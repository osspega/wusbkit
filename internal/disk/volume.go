package disk

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const (
	labelMaxRetries = 3
	labelRetryDelay = 500 * time.Millisecond
)

var procSetVolumeLabelW = syscall.NewLazyDLL("kernel32.dll").NewProc("SetVolumeLabelW")

// SetVolumeLabel sets the volume label on a Windows drive using the native API.
// Retries up to 3 times with a 500ms delay to handle transient USB bus errors.
func SetVolumeLabel(driveLetter, label string) error {
	rootPath := driveLetter + ":\\"
	rootPtr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		return fmt.Errorf("invalid drive letter: %w", err)
	}
	labelPtr, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return fmt.Errorf("invalid label: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < labelMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(labelRetryDelay)
		}
		r1, _, callErr := procSetVolumeLabelW.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(labelPtr)),
		)
		if r1 != 0 {
			return nil
		}
		lastErr = callErr
	}
	return fmt.Errorf("SetVolumeLabelW failed after %d attempts: %w", labelMaxRetries, lastErr)
}
