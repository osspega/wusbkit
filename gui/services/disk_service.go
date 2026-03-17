package services

import (
	"context"
	"fmt"
	"strings"
	"syscall"

	"github.com/lazaroagomez/wusbkit/internal/disk"
	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"golang.org/x/sys/windows"
)

// DiskService handles eject and label operations.
type DiskService struct {
	ctx  context.Context
	enum *usb.Enumerator
}

func NewDiskService() *DiskService {
	return &DiskService{
		enum: usb.NewEnumerator(),
	}
}

func (s *DiskService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// IsAdmin checks if the process has admin privileges.
func (s *DiskService) IsAdmin() bool {
	return format.IsAdmin()
}

// EjectDisk safely ejects a USB drive.
func (s *DiskService) EjectDisk(diskNumber int) error {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, diskNumber)
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("invalid disk path: %w", err)
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return fmt.Errorf("failed to open disk: %w", err)
	}
	defer windows.CloseHandle(handle)

	var bytesReturned uint32
	err = windows.DeviceIoControl(
		handle,
		disk.IOCTL_STORAGE_EJECT_MEDIA,
		nil, 0,
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("eject failed: %w", err)
	}

	return nil
}

// SetLabel sets the volume label on a drive.
func (s *DiskService) SetLabel(diskNumber int, label string) error {
	device, err := s.enum.GetDeviceByDiskNumber(diskNumber)
	if err != nil {
		return err
	}

	driveLetter := strings.TrimSuffix(device.DriveLetter, ":")
	if driveLetter == "" {
		return fmt.Errorf("disk %d has no drive letter", diskNumber)
	}

	return disk.SetVolumeLabel(driveLetter, label)
}

// SetLabels sets labels on multiple drives.
func (s *DiskService) SetLabels(opts LabelOptionsDTO) error {
	for _, diskNum := range opts.DiskNumbers {
		if err := s.SetLabel(diskNum, opts.Label); err != nil {
			return fmt.Errorf("disk %d: %v", diskNum, err)
		}
	}
	return nil
}
