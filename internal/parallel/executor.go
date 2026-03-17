package parallel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lazaroagomez/wusbkit/internal/flash"
	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/lock"
)

var procSetVolumeLabelW = syscall.NewLazyDLL("kernel32.dll").NewProc("SetVolumeLabelW")

const (
	labelMaxRetries = 3
	labelRetryDelay = 500 * time.Millisecond
)

// setVolumeLabel sets the volume label using the native Windows API.
// Retries up to 3 times with 500ms delay to handle USB bus contention
// when labeling multiple drives on the same controller.
func setVolumeLabel(driveLetter, label string) error {
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

// LabelOptions contains options for labeling drives
type LabelOptions struct {
	Label string
}

// OperationResult represents the result of a single disk operation
type OperationResult struct {
	DiskNumber  int    `json:"diskNumber,omitempty"`
	DriveLetter string `json:"driveLetter,omitempty"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	Duration    string `json:"duration"`
}

// BatchResult represents the result of a batch operation
type BatchResult struct {
	Results   []OperationResult `json:"results"`
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
}

// ProgressEvent represents a progress event for NDJSON streaming
type ProgressEvent struct {
	Type        string `json:"type"`                  // "start", "progress", "complete", "summary"
	DiskNumber  int    `json:"diskNumber,omitempty"`  // Only for disk-specific events
	DriveLetter string `json:"driveLetter,omitempty"` // Only for drive-specific events (label)
	Operation   string `json:"operation,omitempty"`   // "format", "flash", or "label"
	Success     bool   `json:"success,omitempty"`
	Error       string `json:"error,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Percentage  int    `json:"percentage,omitempty"`
	// For summary
	Total     int `json:"total,omitempty"`
	Succeeded int `json:"succeeded,omitempty"`
	Failed    int `json:"failed,omitempty"`
}

// Executor handles parallel format/flash operations
type Executor struct {
	maxConcurrent int
	jsonOutput    bool
}

// NewExecutor creates a new parallel executor
// maxConcurrent: max concurrent operations (0 = unlimited)
// jsonOutput: if true, emit NDJSON progress events
func NewExecutor(maxConcurrent int, jsonOutput bool) *Executor {
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // Effectively unlimited
	}
	return &Executor{
		maxConcurrent: maxConcurrent,
		jsonOutput:    jsonOutput,
	}
}

// emitEvent outputs a progress event as NDJSON if JSON output is enabled
func (e *Executor) emitEvent(event ProgressEvent) {
	if e.jsonOutput {
		data, _ := json.Marshal(event)
		fmt.Println(string(data))
	}
}

// FormatAll formats multiple disks in parallel
func (e *Executor) FormatAll(ctx context.Context, disks []int, opts format.Options) BatchResult {
	sem := make(chan struct{}, e.maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]OperationResult, len(disks))

	for i, disk := range disks {
		wg.Add(1)
		go func(idx, diskNum int) {
			defer wg.Done()

			// Emit start event
			e.emitEvent(ProgressEvent{
				Type:       "start",
				DiskNumber: diskNum,
				Operation:  "format",
			})

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				results[idx] = OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      "cancelled",
				}
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "format",
					Success:    false,
					Error:      "cancelled",
				})
				return
			}

			start := time.Now()

			// Acquire disk lock
			diskLock, err := lock.NewDiskLock(diskNum)
			if err != nil {
				result := OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      err.Error(),
					Duration:   time.Since(start).String(),
				}
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "format",
					Success:    false,
					Error:      err.Error(),
					Duration:   result.Duration,
				})
				return
			}

			if err := diskLock.TryLock(ctx, 5*time.Second); err != nil {
				result := OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      "disk busy",
					Duration:   time.Since(start).String(),
				}
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "format",
					Success:    false,
					Error:      "disk busy",
					Duration:   result.Duration,
				})
				return
			}
			defer diskLock.Unlock()

			// Create options copy with this disk number
			diskOpts := opts
			diskOpts.DiskNumber = diskNum

			// Execute format
			formatter := format.NewFormatter()
			go func() {
				// Drain progress channel to prevent blocking
				for range formatter.Progress() {
				}
			}()
			err = formatter.Format(ctx, diskOpts)

			result := OperationResult{
				DiskNumber: diskNum,
				Success:    err == nil,
				Error:      errorString(err),
				Duration:   time.Since(start).String(),
			}

			mu.Lock()
			results[idx] = result
			mu.Unlock()

			e.emitEvent(ProgressEvent{
				Type:       "complete",
				DiskNumber: diskNum,
				Operation:  "format",
				Success:    err == nil,
				Error:      errorString(err),
				Duration:   result.Duration,
			})
		}(i, disk)
	}

	wg.Wait()

	// Build summary
	batch := BatchResult{
		Results: results,
		Total:   len(disks),
	}
	for _, r := range results {
		if r.Success {
			batch.Succeeded++
		} else {
			batch.Failed++
		}
	}

	// Emit summary event
	e.emitEvent(ProgressEvent{
		Type:      "summary",
		Total:     batch.Total,
		Succeeded: batch.Succeeded,
		Failed:    batch.Failed,
	})

	return batch
}

// FlashAll flashes the same image to multiple disks in parallel
func (e *Executor) FlashAll(ctx context.Context, disks []int, opts flash.Options) BatchResult {
	sem := make(chan struct{}, e.maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]OperationResult, len(disks))

	for i, disk := range disks {
		wg.Add(1)
		go func(idx, diskNum int) {
			defer wg.Done()

			// Emit start event
			e.emitEvent(ProgressEvent{
				Type:       "start",
				DiskNumber: diskNum,
				Operation:  "flash",
			})

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				results[idx] = OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      "cancelled",
				}
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "flash",
					Success:    false,
					Error:      "cancelled",
				})
				return
			}

			start := time.Now()

			// Acquire disk lock
			diskLock, err := lock.NewDiskLock(diskNum)
			if err != nil {
				result := OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      err.Error(),
					Duration:   time.Since(start).String(),
				}
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "flash",
					Success:    false,
					Error:      err.Error(),
					Duration:   result.Duration,
				})
				return
			}

			if err := diskLock.TryLock(ctx, 5*time.Second); err != nil {
				result := OperationResult{
					DiskNumber: diskNum,
					Success:    false,
					Error:      "disk busy",
					Duration:   time.Since(start).String(),
				}
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:       "complete",
					DiskNumber: diskNum,
					Operation:  "flash",
					Success:    false,
					Error:      "disk busy",
					Duration:   result.Duration,
				})
				return
			}
			defer diskLock.Unlock()

			// Create options copy with this disk number
			diskOpts := opts
			diskOpts.DiskNumber = diskNum

			// Execute flash
			flasher := flash.NewFlasher()
			go func() {
				// Drain progress channel to prevent blocking
				for range flasher.Progress() {
				}
			}()
			_, _, err = flasher.Flash(ctx, diskOpts)

			result := OperationResult{
				DiskNumber: diskNum,
				Success:    err == nil,
				Error:      errorString(err),
				Duration:   time.Since(start).String(),
			}

			mu.Lock()
			results[idx] = result
			mu.Unlock()

			e.emitEvent(ProgressEvent{
				Type:       "complete",
				DiskNumber: diskNum,
				Operation:  "flash",
				Success:    err == nil,
				Error:      errorString(err),
				Duration:   result.Duration,
			})
		}(i, disk)
	}

	wg.Wait()

	// Build summary
	batch := BatchResult{
		Results: results,
		Total:   len(disks),
	}
	for _, r := range results {
		if r.Success {
			batch.Succeeded++
		} else {
			batch.Failed++
		}
	}

	// Emit summary event
	e.emitEvent(ProgressEvent{
		Type:      "summary",
		Total:     batch.Total,
		Succeeded: batch.Succeeded,
		Failed:    batch.Failed,
	})

	return batch
}

// labelStaggerDelay is the delay between starting label operations on different
// drives. This prevents USB bus contention when multiple drives share a controller.
const labelStaggerDelay = 200 * time.Millisecond

// LabelAll labels multiple drives with controlled concurrency.
// Uses a max concurrency of 2 by default for label operations to avoid
// USB bus contention timeouts, with a stagger delay between starts.
func (e *Executor) LabelAll(ctx context.Context, driveLetters []string, opts LabelOptions) BatchResult {
	// Cap label concurrency at 2 unless user explicitly set higher
	maxConc := e.maxConcurrent
	if maxConc > 2 {
		maxConc = 2
	}

	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]OperationResult, len(driveLetters))

	for i, dl := range driveLetters {
		wg.Add(1)

		// Stagger starts to avoid hitting the USB controller simultaneously
		if i > 0 {
			time.Sleep(labelStaggerDelay)
		}

		go func(idx int, driveLetter string) {
			defer wg.Done()

			// Emit start event
			e.emitEvent(ProgressEvent{
				Type:        "start",
				DriveLetter: driveLetter + ":",
				Operation:   "label",
			})

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				results[idx] = OperationResult{
					DriveLetter: driveLetter + ":",
					Success:     false,
					Error:       "cancelled",
				}
				mu.Unlock()
				e.emitEvent(ProgressEvent{
					Type:        "complete",
					DriveLetter: driveLetter + ":",
					Operation:   "label",
					Success:     false,
					Error:       "cancelled",
				})
				return
			}

			start := time.Now()

			// Execute label change using Windows API (has built-in retry)
			err := setVolumeLabel(driveLetter, opts.Label)

			result := OperationResult{
				DriveLetter: driveLetter + ":",
				Success:     err == nil,
				Error:       errorString(err),
				Duration:    time.Since(start).String(),
			}

			mu.Lock()
			results[idx] = result
			mu.Unlock()

			e.emitEvent(ProgressEvent{
				Type:        "complete",
				DriveLetter: driveLetter + ":",
				Operation:   "label",
				Success:     err == nil,
				Error:       errorString(err),
				Duration:    result.Duration,
			})
		}(i, dl)
	}

	wg.Wait()

	// Build summary
	batch := BatchResult{
		Results: results,
		Total:   len(driveLetters),
	}
	for _, r := range results {
		if r.Success {
			batch.Succeeded++
		} else {
			batch.Failed++
		}
	}

	// Emit summary event
	e.emitEvent(ProgressEvent{
		Type:      "summary",
		Total:     batch.Total,
		Succeeded: batch.Succeeded,
		Failed:    batch.Failed,
	})

	return batch
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ParseDisks parses a disk specification string into a list of disk numbers.
// Supports: "2", "2,3,4", "2-6", "2,4-6,8"
func ParseDisks(arg string) ([]int, error) {
	var disks []int
	parts := strings.Split(arg, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			bounds := strings.Split(part, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid range start: %s", bounds[0])
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid range end: %s", bounds[1])
			}
			if start > end {
				return nil, fmt.Errorf("invalid range: start > end (%d > %d)", start, end)
			}
			for i := start; i <= end; i++ {
				disks = append(disks, i)
			}
		} else {
			disk, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid disk number: %s", part)
			}
			disks = append(disks, disk)
		}
	}

	return unique(disks), nil
}

// unique removes duplicate disk numbers while preserving order
func unique(disks []int) []int {
	seen := make(map[int]bool)
	result := make([]int, 0, len(disks))
	for _, d := range disks {
		if !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	return result
}

// IsMultiDiskArg returns true if the argument contains multi-disk syntax
func IsMultiDiskArg(arg string) bool {
	return strings.ContainsAny(arg, ",-")
}

// PrintBatchResult outputs the batch result for non-JSON mode
func PrintBatchResult(result BatchResult, operation string) {
	fmt.Printf("%s %d/%d drives successfully\n", operation, result.Succeeded, result.Total)
	for _, r := range result.Results {
		status := "OK"
		if !r.Success {
			status = "FAILED: " + r.Error
		}
		// Use drive letter if available, otherwise use disk number
		if r.DriveLetter != "" {
			fmt.Printf("  Drive %s: %s (%s)\n", r.DriveLetter, status, r.Duration)
		} else {
			fmt.Printf("  Disk %d: %s (%s)\n", r.DiskNumber, status, r.Duration)
		}
	}
}

// PrintJSONResult outputs the batch result as JSON (non-streaming mode)
func PrintJSONResult(result BatchResult) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
