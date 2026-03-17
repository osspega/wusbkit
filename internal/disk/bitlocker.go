package disk

import (
	"fmt"
	"strings"

	"github.com/StackExchange/wmi"
)

// bitlockerNamespace is the WMI namespace for BitLocker volume encryption.
const bitlockerNamespace = `root\cimv2\Security\MicrosoftVolumeEncryption`

// win32EncryptableVolume maps the WMI Win32_EncryptableVolume class fields we need.
type win32EncryptableVolume struct {
	DriveLetter      string
	ProtectionStatus uint32
}

// BitLockerStatus represents the protection state of a volume.
type BitLockerStatus struct {
	DriveLetter      string
	ProtectionStatus int  // 0=Off, 1=On, 2=Unknown
	IsProtected      bool // true when ProtectionStatus == 1
}

// CheckBitLocker checks if a volume is BitLocker-protected.
// Returns nil status (not an error) if the BitLocker WMI class is not available
// (e.g., on Windows Home editions).
func CheckBitLocker(driveLetter string) (*BitLockerStatus, error) {
	driveLetter = normalizeDriveLetter(driveLetter)

	var results []win32EncryptableVolume
	query := fmt.Sprintf(
		"SELECT DriveLetter, ProtectionStatus FROM Win32_EncryptableVolume WHERE DriveLetter='%s'",
		driveLetter,
	)

	err := wmi.QueryNamespace(query, &results, bitlockerNamespace)
	if err != nil {
		// WMI class not available (Windows Home) or namespace missing.
		// This is expected on editions without BitLocker support.
		if isWMIClassNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("BitLocker WMI query for %s: %w", driveLetter, err)
	}

	if len(results) == 0 {
		return nil, nil
	}

	vol := results[0]
	return &BitLockerStatus{
		DriveLetter:      vol.DriveLetter,
		ProtectionStatus: int(vol.ProtectionStatus),
		IsProtected:      vol.ProtectionStatus == 1,
	}, nil
}

// CheckBitLockerByDisk checks all volumes on a physical disk for BitLocker protection.
// Returns only protected volumes (empty slice if none are protected).
func CheckBitLockerByDisk(diskNumber int) ([]BitLockerStatus, error) {
	letters, err := getVolumeLettersForDisk(diskNumber)
	if err != nil {
		return nil, fmt.Errorf("get volume letters for disk %d: %w", diskNumber, err)
	}

	var protected []BitLockerStatus
	for _, letter := range letters {
		status, err := CheckBitLocker(letter)
		if err != nil {
			return nil, err
		}
		if status != nil && status.IsProtected {
			protected = append(protected, *status)
		}
	}

	return protected, nil
}

// getVolumeLettersForDisk queries MSFT_Partition to find drive letters for a disk.
func getVolumeLettersForDisk(diskNumber int) ([]string, error) {
	type msftPartitionResult struct {
		DriveLetter uint16
		DiskNumber  uint32
	}

	var partitions []msftPartitionResult
	query := fmt.Sprintf(
		"SELECT DriveLetter, DiskNumber FROM MSFT_Partition WHERE DiskNumber = %d",
		diskNumber,
	)

	err := wmi.QueryNamespace(query, &partitions, `root\Microsoft\Windows\Storage`)
	if err != nil {
		return nil, fmt.Errorf("MSFT_Partition query: %w", err)
	}

	var letters []string
	for _, p := range partitions {
		if p.DriveLetter > 0 && p.DriveLetter < 256 {
			letters = append(letters, fmt.Sprintf("%c:", rune(p.DriveLetter)))
		}
	}

	return letters, nil
}

// normalizeDriveLetter ensures the drive letter is in "X:" format.
func normalizeDriveLetter(dl string) string {
	dl = strings.TrimSpace(dl)
	dl = strings.ToUpper(dl)
	dl = strings.TrimSuffix(dl, `\`)
	if len(dl) == 1 {
		dl += ":"
	}
	return dl
}

// isWMIClassNotFound checks whether a WMI error indicates the class or namespace
// is unavailable. This happens on Windows Home editions that lack BitLocker.
func isWMIClassNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// WBEM_E_INVALID_NAMESPACE (0x8004100E) or WBEM_E_INVALID_CLASS (0x80041010)
	// or generic "not found" / "invalid namespace" messages from the wmi package.
	return strings.Contains(msg, "Invalid namespace") ||
		strings.Contains(msg, "Invalid class") ||
		strings.Contains(msg, "0x8004100e") ||
		strings.Contains(msg, "0x80041010") ||
		strings.Contains(msg, "Not found")
}
