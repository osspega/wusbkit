package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// FormatService handles format operations with per-drive independence.
type FormatService struct {
	ctx       context.Context
	activeOps map[int]context.CancelFunc
	mu        sync.Mutex
}

func NewFormatService() *FormatService {
	return &FormatService{
		activeOps: make(map[int]context.CancelFunc),
	}
}

func (s *FormatService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// StartFormat begins a format operation on one or more drives. Non-blocking.
func (s *FormatService) StartFormat(opts FormatOptionsDTO) error {
	if err := format.ValidateFileSystem(opts.FileSystem); err != nil {
		return err
	}

	if !format.IsAdmin() {
		return fmt.Errorf("administrator privileges required")
	}

	if opts.Label == "" {
		opts.Label = "USB"
	}

	for _, diskNum := range opts.DiskNumbers {
		s.mu.Lock()
		if _, busy := s.activeOps[diskNum]; busy {
			s.mu.Unlock()
			return fmt.Errorf("disk %d already has an active operation", diskNum)
		}

		opCtx, cancel := context.WithCancel(context.Background())
		s.activeOps[diskNum] = cancel
		s.mu.Unlock()

		go s.runFormat(opCtx, diskNum, opts)
	}

	return nil
}

// CancelFormat cancels an active format on the given disk.
func (s *FormatService) CancelFormat(diskNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.activeOps[diskNumber]; ok {
		cancel()
	}
}

func (s *FormatService) runFormat(ctx context.Context, diskNumber int, opts FormatOptionsDTO) {
	defer func() {
		s.mu.Lock()
		delete(s.activeOps, diskNumber)
		s.mu.Unlock()
	}()

	// Acquire disk lock
	diskLock, err := lock.NewDiskLock(diskNumber)
	if err != nil {
		s.emitFormatError(diskNumber, fmt.Sprintf("lock error: %v", err))
		return
	}
	if err := diskLock.TryLock(ctx, 2*time.Second); err != nil {
		s.emitFormatError(diskNumber, fmt.Sprintf("disk busy: %v", err))
		return
	}
	defer diskLock.Unlock()

	formatOpts := format.Options{
		DiskNumber: diskNumber,
		FileSystem: opts.FileSystem,
		Label:      opts.Label,
		Quick:      opts.Quick,
	}

	formatter := format.NewFormatter()

	// Start format in a sub-goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- formatter.Format(ctx, formatOpts)
	}()

	// Forward progress events
	for p := range formatter.Progress() {
		runtime.EventsEmit(s.ctx, "format:progress", FormatProgressDTO{
			DiskNumber:  diskNumber,
			DriveLetter: p.Drive,
			Stage:       p.Stage,
			Percentage:  p.Percentage,
			Status:      p.Status,
			Error:       p.Error,
		})
	}

	if err := <-errChan; err != nil {
		s.emitFormatError(diskNumber, err.Error())
	}
}

func (s *FormatService) emitFormatError(diskNumber int, errMsg string) {
	runtime.EventsEmit(s.ctx, "format:progress", FormatProgressDTO{
		DiskNumber: diskNumber,
		Stage:      "Error",
		Status:     "error",
		Error:      errMsg,
	})
}
