package usb

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// deviceCache holds cached device enumeration results
type deviceCache struct {
	devices   []Device
	timestamp time.Time
	mu        sync.RWMutex
}

const cacheTTL = 2 * time.Second

// Enumerator provides USB device enumeration capabilities
type Enumerator struct {
	cache deviceCache
}

// NewEnumerator creates a new USB device enumerator
func NewEnumerator() *Enumerator {
	return &Enumerator{}
}

// ListDevices returns all connected USB storage devices.
// Uses native WMI queries (no PowerShell).
func (e *Enumerator) ListDevices() ([]Device, error) {
	// Check cache first
	e.cache.mu.RLock()
	if time.Since(e.cache.timestamp) < cacheTTL && e.cache.devices != nil {
		devices := e.cache.devices
		e.cache.mu.RUnlock()
		return devices, nil
	}
	e.cache.mu.RUnlock()

	devices, err := e.listDevicesNative()
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate USB devices: %w", err)
	}

	// Update cache
	e.cache.mu.Lock()
	e.cache.devices = devices
	e.cache.timestamp = time.Now()
	e.cache.mu.Unlock()

	return devices, nil
}

// GetDeviceByDriveLetter returns detailed info for a specific USB device
func (e *Enumerator) GetDeviceByDriveLetter(driveLetter string) (*Device, error) {
	// Normalize drive letter (remove colon if present)
	driveLetter = strings.TrimSuffix(strings.ToUpper(driveLetter), ":")
	if len(driveLetter) != 1 || driveLetter[0] < 'A' || driveLetter[0] > 'Z' {
		return nil, fmt.Errorf("invalid drive letter: %s", driveLetter)
	}

	devices, err := e.ListDevices()
	if err != nil {
		return nil, err
	}

	for _, device := range devices {
		if strings.TrimSuffix(device.DriveLetter, ":") == driveLetter {
			return &device, nil
		}
	}

	return nil, fmt.Errorf("USB drive %s: not found", driveLetter)
}

// GetDeviceByDiskNumber returns detailed info for a specific USB device by disk number
func (e *Enumerator) GetDeviceByDiskNumber(diskNumber int) (*Device, error) {
	devices, err := e.ListDevices()
	if err != nil {
		return nil, err
	}

	for _, device := range devices {
		if device.DiskNumber == diskNumber {
			return &device, nil
		}
	}

	return nil, fmt.Errorf("USB disk %d: not found", diskNumber)
}

// GetDevice returns a USB device by disk number or drive letter.
// It accepts identifiers like "2" (disk number) or "E" / "E:" (drive letter).
func (e *Enumerator) GetDevice(identifier string) (*Device, error) {
	// Try to parse as disk number first
	if diskNum, err := strconv.Atoi(identifier); err == nil {
		return e.GetDeviceByDiskNumber(diskNum)
	}
	return e.GetDeviceByDriveLetter(identifier)
}

// IsSystemDisk checks if a disk contains system/boot/recovery partitions.
// Uses WMI to check partition types.
func (e *Enumerator) IsSystemDisk(diskNumber int) (bool, error) {
	// Check if C: drive is on this disk by checking partition-to-logical-disk mapping
	devices, err := e.ListDevices()
	if err != nil {
		return false, err
	}

	for _, device := range devices {
		if device.DiskNumber == diskNumber {
			// If this disk has the C: drive, it's a system disk
			if strings.TrimSuffix(device.DriveLetter, ":") == "C" {
				return true, nil
			}
			return false, nil
		}
	}

	return false, nil
}

// getOperationalStatus converts operational status to string
func (e *Enumerator) getOperationalStatus(status interface{}) string {
	switch v := status.(type) {
	case float64:
		switch int(v) {
		case 2:
			return "Online"
		case 0xD010:
			return "Online"
		case 0xD012:
			return "No Media"
		default:
			return "Unknown"
		}
	case string:
		return v
	default:
		return "Unknown"
	}
}
