package services

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"strings"

	"github.com/lazaroagomez/wusbkit/internal/flash"
	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/iso"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// FlashService handles flash operations with per-drive independence.
type FlashService struct {
	ctx       context.Context
	activeOps map[int]context.CancelFunc
	mu        sync.Mutex
	enum      *usb.Enumerator
}

func NewFlashService() *FlashService {
	return &FlashService{
		activeOps: make(map[int]context.CancelFunc),
		enum:      usb.NewEnumerator(),
	}
}

func (s *FlashService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// OpenImageDialog opens a native file dialog for selecting an image file.
func (s *FlashService) OpenImageDialog() (string, error) {
	return runtime.OpenFileDialog(s.ctx, runtime.OpenDialogOptions{
		Title: "Select Image File",
		Filters: []runtime.FileFilter{
			{DisplayName: "Disk Images (*.img;*.iso;*.bin;*.raw;*.gz;*.xz;*.zst;*.zip)", Pattern: "*.img;*.iso;*.bin;*.raw;*.gz;*.xz;*.zst;*.zstd;*.zip"},
			{DisplayName: "All Files (*.*)", Pattern: "*.*"},
		},
	})
}

// StartFlash begins a flash operation on one or more drives. Non-blocking.
func (s *FlashService) StartFlash(opts FlashOptionsDTO) error {
	if opts.ImagePath == "" {
		return fmt.Errorf("image path is required")
	}

	// Validate image exists (skip for URLs)
	if !flash.IsURL(opts.ImagePath) {
		if _, err := os.Stat(opts.ImagePath); os.IsNotExist(err) {
			return fmt.Errorf("image file not found: %s", opts.ImagePath)
		}
	}

	if !format.IsAdmin() {
		return fmt.Errorf("administrator privileges required")
	}

	if opts.BufferSizeMB < 1 || opts.BufferSizeMB > 64 {
		opts.BufferSizeMB = 4
	}

	// Validate all disks exist
	for _, diskNum := range opts.DiskNumbers {
		if _, err := s.enum.GetDeviceByDiskNumber(diskNum); err != nil {
			return fmt.Errorf("disk %d: %v", diskNum, err)
		}
	}

	// Launch each disk flash in its own goroutine
	for _, diskNum := range opts.DiskNumbers {
		s.mu.Lock()
		if _, busy := s.activeOps[diskNum]; busy {
			s.mu.Unlock()
			return fmt.Errorf("disk %d already has an active operation", diskNum)
		}

		opCtx, cancel := context.WithCancel(context.Background())
		s.activeOps[diskNum] = cancel
		s.mu.Unlock()

		go s.runFlash(opCtx, diskNum, opts)
	}

	return nil
}

// CancelFlash cancels an active flash on the given disk.
func (s *FlashService) CancelFlash(diskNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.activeOps[diskNumber]; ok {
		cancel()
	}
}

func (s *FlashService) runFlash(ctx context.Context, diskNumber int, opts FlashOptionsDTO) {
	defer func() {
		s.mu.Lock()
		delete(s.activeOps, diskNumber)
		s.mu.Unlock()
	}()

	// Acquire disk lock
	diskLock, err := lock.NewDiskLock(diskNumber)
	if err != nil {
		s.emitFlashError(diskNumber, fmt.Sprintf("lock error: %v", err))
		return
	}
	if err := diskLock.TryLock(ctx, 2*time.Second); err != nil {
		s.emitFlashError(diskNumber, fmt.Sprintf("disk busy: %v", err))
		return
	}
	defer diskLock.Unlock()

	// Auto-detect extract mode for Windows ISOs
	useExtract := opts.Extract
	if !useExtract && !flash.IsURL(opts.ImagePath) &&
		strings.HasSuffix(strings.ToLower(opts.ImagePath), ".iso") {
		useExtract = iso.IsWindowsISO(opts.ImagePath)
	}

	if useExtract {
		s.runExtractFlash(ctx, diskNumber, opts)
		return
	}

	// Get drive letter for the flasher
	device, _ := s.enum.GetDeviceByDiskNumber(diskNumber)
	driveLetter := ""
	if device != nil {
		driveLetter = device.DriveLetter
	}

	flashOpts := flash.Options{
		DiskNumber:    diskNumber,
		ImagePath:     opts.ImagePath,
		Verify:        opts.Verify,
		BufferSize:    opts.BufferSizeMB,
		CalculateHash: opts.CalculateHash,
		SkipUnchanged: opts.SkipUnchanged,
		DriveLetter:   driveLetter,
	}

	flasher := flash.NewFlasher()

	// Start flash in a sub-goroutine
	errChan := make(chan error, 1)
	go func() {
		_, _, err := flasher.Flash(ctx, flashOpts)
		errChan <- err
	}()

	// Forward progress events
	for p := range flasher.Progress() {
		runtime.EventsEmit(s.ctx, "flash:progress", FlashProgressDTO{
			DiskNumber:   diskNumber,
			Stage:        p.Stage,
			Percentage:   p.Percentage,
			BytesWritten: p.BytesWritten,
			TotalBytes:   p.TotalBytes,
			Speed:        p.Speed,
			Status:       p.Status,
			Error:        p.Error,
			Hash:         p.Hash,
			BytesSkipped: p.BytesSkipped,
		})
	}

	// Check final error
	if err := <-errChan; err != nil {
		s.emitFlashError(diskNumber, err.Error())
	}
}

// runExtractFlash uses the ISO pipeline to partition, format, and extract ISO contents.
func (s *FlashService) runExtractFlash(ctx context.Context, diskNumber int, opts FlashOptionsDTO) {
	writer := iso.NewWriter()

	errChan := make(chan error, 1)
	go func() {
		errChan <- writer.Write(ctx, iso.WriteOptions{
			DiskNumber: diskNumber,
			ISOPath:    opts.ImagePath,
		})
	}()

	for p := range writer.Progress() {
		status := "in_progress"
		if p.Error != "" {
			status = "error"
		} else if p.Percentage >= 100 {
			status = "complete"
		}
		runtime.EventsEmit(s.ctx, "flash:progress", FlashProgressDTO{
			DiskNumber: diskNumber,
			Stage:      p.Stage,
			Percentage: p.Percentage,
			Status:     status,
			Error:      p.Error,
		})
	}

	if err := <-errChan; err != nil {
		s.emitFlashError(diskNumber, err.Error())
	}
}

func (s *FlashService) emitFlashError(diskNumber int, errMsg string) {
	runtime.EventsEmit(s.ctx, "flash:progress", FlashProgressDTO{
		DiskNumber: diskNumber,
		Stage:      "Error",
		Status:     "error",
		Error:      errMsg,
	})
}
