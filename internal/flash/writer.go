package flash

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/StackExchange/wmi"
	"golang.org/x/sys/windows"
)

// BufferPool manages reusable aligned buffers to reduce allocations
// during parallel flash operations.
type BufferPool struct {
	sync.RWMutex
	pools map[int]*sync.Pool
}

var globalBufferPool = &BufferPool{
	pools: make(map[int]*sync.Pool),
}

// GetBuffer returns an aligned buffer of the specified size from the pool.
// If no buffer is available, a new one is allocated.
func GetBuffer(size int) []byte {
	globalBufferPool.RLock()
	pool, exists := globalBufferPool.pools[size]
	globalBufferPool.RUnlock()

	if !exists {
		globalBufferPool.Lock()
		// Double-check after acquiring write lock
		if pool, exists = globalBufferPool.pools[size]; !exists {
			pool = &sync.Pool{
				New: func() interface{} {
					return alignedBuffer(size)
				},
			}
			globalBufferPool.pools[size] = pool
		}
		globalBufferPool.Unlock()
	}

	return pool.Get().([]byte)
}

// PutBuffer returns a buffer to the pool for reuse.
// The buffer is cleared before being returned to the pool.
func PutBuffer(size int, buf []byte) {
	if buf == nil || len(buf) != size {
		return
	}

	// Clear buffer before returning to pool
	clear(buf)

	globalBufferPool.RLock()
	pool, exists := globalBufferPool.pools[size]
	globalBufferPool.RUnlock()

	if exists {
		pool.Put(buf)
	}
}

// Win32_DiskDriveToDiskPartition represents WMI association
type Win32_DiskDriveToDiskPartition struct {
	Antecedent string
	Dependent  string
}

// Win32_LogicalDiskToPartitionAssoc represents WMI association
type Win32_LogicalDiskToPartitionAssoc struct {
	Antecedent string
	Dependent  string
}

const (
	// Buffer size for read/write operations (4 MB, matching Linux recovery script)
	bufferSize = 4 << 20

	// Alignment required for unbuffered I/O (4 KB)
	alignment = 4096

	// Windows IOCTL codes
	FSCTL_LOCK_VOLUME    = 0x00090018
	FSCTL_DISMOUNT_VOLUME = 0x00090020
	FSCTL_ALLOW_EXTENDED_DASD_IO = 0x00090083
)

// diskWriter handles raw disk write operations on Windows
type diskWriter struct {
	diskNumber       int
	handle           windows.Handle
	volumes          []windows.Handle
	cachedDriveLetter string // Optional: pre-cached drive letter to avoid lookups
}

// newDiskWriter creates a writer for raw disk access
func newDiskWriter(diskNumber int) *diskWriter {
	return &diskWriter{
		diskNumber: diskNumber,
		handle:     windows.InvalidHandle,
		volumes:    make([]windows.Handle, 0),
	}
}

// newDiskWriterWithDriveLetter creates a writer with a pre-cached drive letter
// to avoid the PowerShell/WMI lookup during volume locking.
func newDiskWriterWithDriveLetter(diskNumber int, driveLetter string) *diskWriter {
	// Normalize drive letter (remove colon if present)
	driveLetter = strings.TrimSuffix(strings.ToUpper(driveLetter), ":")
	return &diskWriter{
		diskNumber:       diskNumber,
		handle:           windows.InvalidHandle,
		volumes:          make([]windows.Handle, 0),
		cachedDriveLetter: driveLetter,
	}
}

// Open prepares the disk for writing by locking and dismounting volumes
func (w *diskWriter) Open() error {
	// First, lock and dismount all volumes on this disk
	if err := w.lockVolumes(); err != nil {
		return fmt.Errorf("failed to lock volumes: %w", err)
	}

	// Open the physical disk for writing
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, w.diskNumber)
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
		windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	if err != nil {
		w.Close()
		return fmt.Errorf("failed to open disk: %w", err)
	}

	w.handle = handle

	// Enable extended DASD I/O for large disks
	var bytesReturned uint32
	_ = windows.DeviceIoControl(
		w.handle,
		FSCTL_ALLOW_EXTENDED_DASD_IO,
		nil, 0, nil, 0,
		&bytesReturned, nil,
	)

	return nil
}

// lockVolumes finds and locks all volumes on this physical disk
func (w *diskWriter) lockVolumes() error {
	// Get volume letters for this disk via PowerShell
	letters, err := w.getVolumeLetters()
	if err != nil {
		// Non-fatal: disk might not have volumes
		return nil
	}

	for _, letter := range letters {
		volumePath := fmt.Sprintf(`\\.\%s:`, letter)
		pathPtr, err := syscall.UTF16PtrFromString(volumePath)
		if err != nil {
			continue
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
			continue
		}

		// Lock the volume
		var bytesReturned uint32
		err = windows.DeviceIoControl(
			handle,
			FSCTL_LOCK_VOLUME,
			nil, 0, nil, 0,
			&bytesReturned, nil,
		)
		if err != nil {
			windows.CloseHandle(handle)
			continue
		}

		// Dismount the volume
		_ = windows.DeviceIoControl(
			handle,
			FSCTL_DISMOUNT_VOLUME,
			nil, 0, nil, 0,
			&bytesReturned, nil,
		)

		w.volumes = append(w.volumes, handle)
	}

	return nil
}

// getVolumeLetters returns drive letters for volumes on this disk.
// Uses cached drive letter if available, otherwise queries WMI (faster than PowerShell).
func (w *diskWriter) getVolumeLetters() ([]string, error) {
	// Use cached drive letter if available (fastest path)
	if w.cachedDriveLetter != "" {
		return []string{w.cachedDriveLetter}, nil
	}

	// Use WMI to get volume letters (faster than PowerShell)
	return getVolumeLettersWMI(w.diskNumber)
}

// getVolumeLettersWMI queries WMI to find drive letters for a given disk number.
// This is faster than spawning a PowerShell process.
func getVolumeLettersWMI(diskNumber int) ([]string, error) {
	// Query partition associations for this disk
	type MSFT_PartitionResult struct {
		DriveLetter uint16
		DiskNumber  uint32
	}

	// Try Storage WMI first (MSFT_Partition in root\Microsoft\Windows\Storage)
	var partitions []MSFT_PartitionResult
	query := fmt.Sprintf("SELECT DriveLetter, DiskNumber FROM MSFT_Partition WHERE DiskNumber = %d", diskNumber)
	err := wmi.QueryNamespace(query, &partitions, `root\Microsoft\Windows\Storage`)
	if err == nil && len(partitions) > 0 {
		var letters []string
		for _, p := range partitions {
			if p.DriveLetter > 0 && p.DriveLetter < 256 {
				letters = append(letters, string(rune(p.DriveLetter)))
			}
		}
		if len(letters) > 0 {
			return letters, nil
		}
	}

	// Fallback: Use Win32_DiskDrive -> Win32_DiskPartition -> Win32_LogicalDisk associations
	// This is more complex but works on all Windows versions
	type DiskPartition struct {
		DeviceID  string
		DiskIndex uint32
	}

	type LogicalDiskAssoc struct {
		Antecedent string
		Dependent  string
	}

	// Get partitions for this disk
	var diskPartitions []DiskPartition
	partQuery := fmt.Sprintf("SELECT DeviceID, DiskIndex FROM Win32_DiskPartition WHERE DiskIndex = %d", diskNumber)
	if err := wmi.Query(partQuery, &diskPartitions); err != nil {
		return nil, nil // Non-fatal: disk might not have partitions
	}

	if len(diskPartitions) == 0 {
		return nil, nil
	}

	// Get logical disk to partition associations
	var assocs []LogicalDiskAssoc
	wmi.Query("SELECT Antecedent, Dependent FROM Win32_LogicalDiskToPartition", &assocs)

	// Build map of partition DeviceID to drive letter
	var letters []string
	for _, part := range diskPartitions {
		for _, assoc := range assocs {
			// Check if this association references our partition
			if strings.Contains(assoc.Antecedent, part.DeviceID) {
				// Extract drive letter from Dependent (e.g., "...Win32_LogicalDisk.DeviceID=\"E:\"")
				idx := strings.Index(assoc.Dependent, `DeviceID="`)
				if idx != -1 {
					start := idx + len(`DeviceID="`)
					end := strings.Index(assoc.Dependent[start:], `"`)
					if end > 0 {
						driveLetter := assoc.Dependent[start : start+end]
						driveLetter = strings.TrimSuffix(driveLetter, ":")
						if len(driveLetter) == 1 {
							letters = append(letters, driveLetter)
						}
					}
				}
			}
		}
	}

	return letters, nil
}

// WriteAt writes data at the specified offset (must be aligned to 4096)
func (w *diskWriter) WriteAt(data []byte, offset int64) (int, error) {
	if w.handle == windows.InvalidHandle {
		return 0, fmt.Errorf("disk not opened")
	}

	// Seek to position
	_, err := windows.Seek(w.handle, offset, 0)
	if err != nil {
		return 0, fmt.Errorf("seek failed: %w", err)
	}

	// Write data
	var written uint32
	err = windows.WriteFile(w.handle, data, &written, nil)
	if err != nil {
		return int(written), fmt.Errorf("write failed: %w", err)
	}

	return int(written), nil
}

// ReadAt reads data at the specified offset (for verification)
func (w *diskWriter) ReadAt(data []byte, offset int64) (int, error) {
	if w.handle == windows.InvalidHandle {
		return 0, fmt.Errorf("disk not opened")
	}

	// Seek to position
	_, err := windows.Seek(w.handle, offset, 0)
	if err != nil {
		return 0, fmt.Errorf("seek failed: %w", err)
	}

	// Read data
	var read uint32
	err = windows.ReadFile(w.handle, data, &read, nil)
	if err != nil {
		return int(read), fmt.Errorf("read failed: %w", err)
	}

	return int(read), nil
}

// Close releases all handles
func (w *diskWriter) Close() error {
	// Close volume handles (unlocks them)
	for _, h := range w.volumes {
		windows.CloseHandle(h)
	}
	w.volumes = nil

	// Close disk handle
	if w.handle != windows.InvalidHandle {
		windows.CloseHandle(w.handle)
		w.handle = windows.InvalidHandle
	}

	return nil
}

// alignedBuffer allocates a buffer aligned to 4096 bytes for unbuffered I/O.
// Go's runtime allocator returns naturally word-aligned memory, so the backing
// array of the slice will be at least 8-byte aligned. We over-allocate by
// `alignment` bytes and find the first 4096-aligned offset within the slice.
func alignedBuffer(size int) []byte {
	// Allocate extra space for alignment
	buf := make([]byte, size+alignment)

	// Calculate aligned start
	addr := uintptr(unsafe.Pointer(&buf[0]))
	offset := (alignment - int(addr%alignment)) % alignment

	return buf[offset : offset+size]
}

// alignSize rounds up size to the nearest alignment boundary
func alignSize(size int) int {
	return ((size + alignment - 1) / alignment) * alignment
}
