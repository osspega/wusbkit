package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/image"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ImageService handles image creation operations.
type ImageService struct {
	ctx       context.Context
	activeOps map[int]context.CancelFunc
	mu        sync.Mutex
}

func NewImageService() *ImageService {
	return &ImageService{
		activeOps: make(map[int]context.CancelFunc),
	}
}

func (s *ImageService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// StartCreateImage begins creating a .bin image from a USB drive. Non-blocking.
func (s *ImageService) StartCreateImage(opts CreateImageOptionsDTO) error {
	if opts.OutputPath == "" {
		return fmt.Errorf("output path is required")
	}

	if !format.IsAdmin() {
		return fmt.Errorf("administrator privileges required")
	}

	s.mu.Lock()
	if _, busy := s.activeOps[opts.DiskNumber]; busy {
		s.mu.Unlock()
		return fmt.Errorf("disk %d already has an active operation", opts.DiskNumber)
	}

	opCtx, cancel := context.WithCancel(context.Background())
	s.activeOps[opts.DiskNumber] = cancel
	s.mu.Unlock()

	go s.runCreateImage(opCtx, opts)
	return nil
}

// CancelCreateImage cancels an active image creation.
func (s *ImageService) CancelCreateImage(diskNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.activeOps[diskNumber]; ok {
		cancel()
	}
}

// OpenSaveDialog opens a native file save dialog.
func (s *ImageService) OpenSaveDialog() (string, error) {
	return runtime.SaveFileDialog(s.ctx, runtime.SaveDialogOptions{
		Title:           "Save Image As",
		DefaultFilename: "image.bin",
		Filters: []runtime.FileFilter{
			{DisplayName: "Binary Images (*.bin)", Pattern: "*.bin"},
			{DisplayName: "All Files (*.*)", Pattern: "*.*"},
		},
	})
}

func (s *ImageService) runCreateImage(ctx context.Context, opts CreateImageOptionsDTO) {
	defer func() {
		s.mu.Lock()
		delete(s.activeOps, opts.DiskNumber)
		s.mu.Unlock()
	}()

	diskLock, err := lock.NewDiskLock(opts.DiskNumber)
	if err != nil {
		s.emitError(opts.DiskNumber, fmt.Sprintf("lock error: %v", err))
		return
	}
	if err := diskLock.TryLock(ctx, 2*time.Second); err != nil {
		s.emitError(opts.DiskNumber, fmt.Sprintf("disk busy: %v", err))
		return
	}
	defer diskLock.Unlock()

	creator := image.NewCreator()

	errChan := make(chan error, 1)
	go func() {
		errChan <- creator.Create(ctx, image.CreateOptions{
			DiskNumber: opts.DiskNumber,
			OutputPath: opts.OutputPath,
			Verify:     opts.Verify,
		})
	}()

	for p := range creator.Progress() {
		runtime.EventsEmit(s.ctx, "image:progress", ImageProgressDTO{
			DiskNumber: opts.DiskNumber,
			Stage:      p.Stage,
			Percentage: p.Percentage,
			BytesRead:  p.BytesRead,
			TotalBytes: p.TotalBytes,
			Speed:      p.Speed,
			Status:     p.Status,
			Error:      p.Error,
		})
	}

	if err := <-errChan; err != nil {
		s.emitError(opts.DiskNumber, err.Error())
	}
}

func (s *ImageService) emitError(diskNumber int, errMsg string) {
	runtime.EventsEmit(s.ctx, "image:progress", ImageProgressDTO{
		DiskNumber: diskNumber,
		Stage:      "Error",
		Status:     "error",
		Error:      errMsg,
	})
}
