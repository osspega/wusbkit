package disk

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Virtual Disk API constants.
const (
	virtualStorageTypeDeviceISO = 1

	virtualDiskAccessAttachRO = 0x00010000

	attachVirtualDiskFlagReadOnly = 0x00000001

	openVirtualDiskVersion1 = 1
)

// VIRTUAL_STORAGE_TYPE_VENDOR_MICROSOFT GUID: {EC984AEC-A0F9-47e9-901F-71415A66345B}
var vendorMicrosoft = [16]byte{
	0xEC, 0x4A, 0x98, 0xEC, 0xF9, 0xA0, 0xe9, 0x47,
	0x90, 0x1F, 0x71, 0x41, 0x5A, 0x66, 0x34, 0x5B,
}

var virtdiskDLL = windows.NewLazySystemDLL("virtdisk.dll")
var (
	procOpenVirtualDisk            = virtdiskDLL.NewProc("OpenVirtualDisk")
	procAttachVirtualDisk          = virtdiskDLL.NewProc("AttachVirtualDisk")
	procDetachVirtualDisk          = virtdiskDLL.NewProc("DetachVirtualDisk")
	procGetVirtualDiskPhysicalPath = virtdiskDLL.NewProc("GetVirtualDiskPhysicalPath")
)

// rawVirtualStorageType maps to VIRTUAL_STORAGE_TYPE.
type rawVirtualStorageType struct {
	DeviceId uint32
	VendorId [16]byte
}

// rawOpenVirtualDiskParametersV1 maps to OPEN_VIRTUAL_DISK_PARAMETERS (Version 1).
type rawOpenVirtualDiskParametersV1 struct {
	Version uint32
	RWDepth uint32
}

// MountISO mounts an ISO file read-only using the Windows Virtual Disk API
// and returns the drive letter (e.g. "F:\") and a cleanup function that
// unmounts the ISO when called.
func MountISO(isoPath string) (string, func(), error) {
	pathPtr, err := windows.UTF16PtrFromString(isoPath)
	if err != nil {
		return "", nil, fmt.Errorf("invalid ISO path: %w", err)
	}

	storageType := rawVirtualStorageType{
		DeviceId: virtualStorageTypeDeviceISO,
		VendorId: vendorMicrosoft,
	}

	params := rawOpenVirtualDiskParametersV1{
		Version: openVirtualDiskVersion1,
		RWDepth: 0,
	}

	var handle windows.Handle
	r, _, _ := procOpenVirtualDisk.Call(
		uintptr(unsafe.Pointer(&storageType)),
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(virtualDiskAccessAttachRO),
		0, // OpenFlags
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if r != 0 {
		return "", nil, fmt.Errorf("OpenVirtualDisk: %w", windows.Errno(r))
	}

	// Attach read-only (auto-assigns drive letter).
	r, _, _ = procAttachVirtualDisk.Call(
		uintptr(handle),
		0, // SecurityDescriptor
		uintptr(attachVirtualDiskFlagReadOnly),
		0,    // ProviderSpecificFlags
		0,    // AttachParameters (nil = default)
		0,    // Overlapped
	)
	if r != 0 {
		windows.CloseHandle(handle)
		return "", nil, fmt.Errorf("AttachVirtualDisk: %w", windows.Errno(r))
	}

	// Get the physical path (e.g. \\.\PhysicalDrive5).
	diskNumber, err := getVirtualDiskNumber(handle)
	if err != nil {
		detachAndClose(handle)
		return "", nil, err
	}

	// Poll for the volume and drive letter.
	driveLetter, err := waitForISODriveLetter(diskNumber, 10*time.Second)
	if err != nil {
		detachAndClose(handle)
		return "", nil, err
	}

	cleanup := func() { detachAndClose(handle) }
	return driveLetter, cleanup, nil
}

// getVirtualDiskNumber retrieves the physical disk number from a mounted
// virtual disk handle by calling GetVirtualDiskPhysicalPath.
func getVirtualDiskNumber(handle windows.Handle) (int, error) {
	buf := make([]uint16, 260)
	bufSize := uint32(len(buf) * 2) // size in bytes

	r, _, _ := procGetVirtualDiskPhysicalPath.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r != 0 {
		return 0, fmt.Errorf("GetVirtualDiskPhysicalPath: %w", windows.Errno(r))
	}

	// Parse "\\.\PhysicalDriveN" → N
	physPath := windows.UTF16ToString(buf)
	const prefix = `\\.\PhysicalDrive`
	if !strings.HasPrefix(physPath, prefix) {
		return 0, fmt.Errorf("unexpected physical path: %s", physPath)
	}
	num, err := strconv.Atoi(physPath[len(prefix):])
	if err != nil {
		return 0, fmt.Errorf("parse disk number from %q: %w", physPath, err)
	}
	return num, nil
}

// waitForISODriveLetter polls for a drive letter on the given disk number.
func waitForISODriveLetter(diskNumber int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		volumePath, err := FindVolumeByDiskNumber(diskNumber)
		if err == nil && volumePath != "" {
			letter, err := GetVolumeDriveLetter(volumePath)
			if err == nil && letter != "" {
				return letter, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for ISO drive letter on PhysicalDrive%d", diskNumber)
}

// detachAndClose detaches the virtual disk and closes the handle.
func detachAndClose(handle windows.Handle) {
	procDetachVirtualDisk.Call(uintptr(handle), 0, 0)
	windows.CloseHandle(handle)
}
