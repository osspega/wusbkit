package flash

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lazaroagomez/wusbkit/internal/disk"
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

// lockVolumes finds and locks all volumes on this physical disk.
// Uses volume GUID enumeration (via IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS)
// which finds all volumes regardless of whether they have drive letters.
func (w *diskWriter) lockVolumes() error {
	// Try cached drive letter first (fastest path for single-partition drives)
	if w.cachedDriveLetter != "" {
		w.lockSingleVolume(fmt.Sprintf(`\\.\%s:`, w.cachedDriveLetter))
	}

	// Enumerate ALL volumes on this disk by GUID path — catches partitions
	// without drive letters (e.g. ChromeOS, multi-partition layouts).
	for _, guidPath := range disk.FindAllVolumesByDiskNumber(w.diskNumber) {
		devPath := strings.TrimRight(guidPath, `\`)
		w.lockSingleVolume(devPath)
	}

	return nil
}

// lockSingleVolume opens, locks, and dismounts a single volume path.
func (w *diskWriter) lockSingleVolume(volumePath string) {
	pathPtr, err := syscall.UTF16PtrFromString(volumePath)
	if err != nil {
		return
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
		return
	}

	var bytesReturned uint32
	err = windows.DeviceIoControl(
		handle,
		FSCTL_LOCK_VOLUME,
		nil, 0, nil, 0,
		&bytesReturned, nil,
	)
	if err != nil {
		windows.CloseHandle(handle)
		return
	}

	_ = windows.DeviceIoControl(
		handle,
		FSCTL_DISMOUNT_VOLUME,
		nil, 0, nil, 0,
		&bytesReturned, nil,
	)

	w.volumes = append(w.volumes, handle)
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
