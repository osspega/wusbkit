package disk

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// FormatVolumeOptions configures an NTFS or exFAT format operation using the
// Windows fmifs.dll FormatEx function or, as a fallback, the VDS COM API.
type FormatVolumeOptions struct {
	// VolumePath is the volume to format, e.g. `\\?\Volume{GUID}\` or `E:\`.
	VolumePath string

	// FileSystem is the target filesystem: "NTFS" or "exFAT".
	FileSystem string

	// Label is the volume label (max 32 chars for NTFS, 11 for exFAT).
	Label string

	// QuickFormat performs a quick format when true.
	QuickFormat bool

	// ClusterSize is the allocation unit size in bytes. Use 0 for the default.
	ClusterSize uint32
}

// FormatVolume formats a volume as NTFS or exFAT. It first attempts the
// fmifs.dll FormatEx approach (simpler, no COM). If that fails, it falls
// back to the VDS COM API.
func FormatVolume(opts FormatVolumeOptions) error {
	if err := validateFormatOptions(opts); err != nil {
		return fmt.Errorf("invalid format options: %w", err)
	}

	err := formatViaFmifs(opts)
	if err == nil {
		return nil
	}

	// fmifs failed -- try VDS COM as fallback.
	vdsErr := formatViaVDS(opts)
	if vdsErr != nil {
		return fmt.Errorf("fmifs format failed (%w); VDS fallback also failed: %w", err, vdsErr)
	}
	return nil
}

// FormatVolumeWithFmifs formats using only the fmifs.dll approach.
func FormatVolumeWithFmifs(opts FormatVolumeOptions) error {
	if err := validateFormatOptions(opts); err != nil {
		return fmt.Errorf("invalid format options: %w", err)
	}
	return formatViaFmifs(opts)
}

// FormatVolumeWithVDS formats using only the VDS COM approach.
func FormatVolumeWithVDS(opts FormatVolumeOptions) error {
	if err := validateFormatOptions(opts); err != nil {
		return fmt.Errorf("invalid format options: %w", err)
	}
	return formatViaVDS(opts)
}

func validateFormatOptions(opts FormatVolumeOptions) error {
	if opts.VolumePath == "" {
		return fmt.Errorf("volume path is required")
	}
	fs := strings.ToUpper(opts.FileSystem)
	if fs != "NTFS" && fs != "EXFAT" {
		return fmt.Errorf("unsupported filesystem %q (supported: NTFS, exFAT)", opts.FileSystem)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Option A: fmifs.dll FormatEx
// ---------------------------------------------------------------------------

// fmifsCallbackCommand enumerates the callback commands sent by FormatEx.
type fmifsCallbackCommand uint32

const (
	fmifsProgress             fmifsCallbackCommand = 0x00
	fmifsDoneWithStructure    fmifsCallbackCommand = 0x06
	fmifsIncompatibleFileSystem fmifsCallbackCommand = 0x07
	fmifsAccessDenied         fmifsCallbackCommand = 0x08
	fmifsMediaWriteProtected  fmifsCallbackCommand = 0x09
	fmifsVolumeInUse          fmifsCallbackCommand = 0x0A
	fmifsDone                 fmifsCallbackCommand = 0x0B
	fmifsOutput               fmifsCallbackCommand = 0x0B
)

// fmifsMediaType for hard disks.
const fmifsHardDisk = 0x0C

// formatResult captures the outcome of an fmifs FormatEx callback sequence.
type formatResult struct {
	mu       sync.Mutex
	success  bool
	finished bool
	lastErr  fmifsCallbackCommand
}

// There can only be one fmifs format operation at a time (the callback is a
// global function pointer, not a closure). This mutex serializes access.
var fmifsFormatMu sync.Mutex

// globalFormatResult is written by the fmifs callback and read after FormatEx
// returns. Protected by fmifsFormatMu (only one format at a time).
var globalFormatResult formatResult

// fmifsCallback is the callback function passed to FormatEx.
// It is called from the fmifs.dll thread with progress and status updates.
//
// The actionInfo parameter is a pointer to command-specific data. For the
// DONE command it points to a BOOLEAN indicating success. Converting from
// uintptr to unsafe.Pointer here is necessary because the pointer originates
// from native code via the callback mechanism.
func fmifsCallback(command fmifsCallbackCommand, _ uint32, actionInfo uintptr) uintptr {
	switch command {
	case fmifsProgress:
		// actionInfo points to a DWORD percentage -- we ignore it for now.
	case fmifsDone:
		globalFormatResult.mu.Lock()
		globalFormatResult.finished = true
		// actionInfo points to a BOOLEAN (4 bytes). Non-zero means success.
		if actionInfo != 0 {
			globalFormatResult.success = readUint32Ptr(actionInfo) != 0
		}
		globalFormatResult.mu.Unlock()
	case fmifsIncompatibleFileSystem, fmifsAccessDenied,
		fmifsMediaWriteProtected, fmifsVolumeInUse:
		globalFormatResult.mu.Lock()
		globalFormatResult.lastErr = command
		globalFormatResult.mu.Unlock()
	}
	// Return TRUE (1) to continue the operation.
	return 1
}

// readUint32Ptr reads a uint32 value from the address given as a uintptr.
// This is used in the fmifs callback where we receive native pointers as
// uintptr arguments.
//
//go:nosplit
func readUint32Ptr(addr uintptr) uint32 {
	return *(*uint32)(unsafe.Pointer(addr)) //nolint:govet
}

func formatViaFmifs(opts FormatVolumeOptions) error {
	fmifsFormatMu.Lock()
	defer fmifsFormatMu.Unlock()

	// Reset global state.
	globalFormatResult = formatResult{}

	fmifsDLL := windows.NewLazySystemDLL("fmifs.dll")
	procFormatEx := fmifsDLL.NewProc("FormatEx")
	if err := procFormatEx.Find(); err != nil {
		return fmt.Errorf("fmifs.dll!FormatEx not found: %w", err)
	}

	// Ensure the volume path ends with a backslash.
	volumePath := opts.VolumePath
	if !strings.HasSuffix(volumePath, `\`) {
		volumePath += `\`
	}

	driveRoot, err := syscall.UTF16PtrFromString(volumePath)
	if err != nil {
		return fmt.Errorf("invalid volume path: %w", err)
	}

	fsName := strings.ToUpper(opts.FileSystem)
	// The fmifs.dll expects "exFAT" with that exact casing.
	if fsName == "EXFAT" {
		fsName = "exFAT"
	}
	fsNamePtr, err := syscall.UTF16PtrFromString(fsName)
	if err != nil {
		return fmt.Errorf("invalid filesystem name: %w", err)
	}

	label := opts.Label
	if label == "" {
		label = "USB"
	}
	labelPtr, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return fmt.Errorf("invalid label: %w", err)
	}

	quickFormat := uintptr(0)
	if opts.QuickFormat {
		quickFormat = 1
	}

	callbackPtr := syscall.NewCallback(fmifsCallback)

	// FormatEx(DriveRoot, MediaType, FileSystemName, Label, QuickFormat,
	//          ClusterSize, Callback)
	//
	// All parameters are passed as uintptr per the syscall convention.
	procFormatEx.Call(
		uintptr(unsafe.Pointer(driveRoot)),
		uintptr(fmifsHardDisk),
		uintptr(unsafe.Pointer(fsNamePtr)),
		uintptr(unsafe.Pointer(labelPtr)),
		quickFormat,
		uintptr(opts.ClusterSize),
		callbackPtr,
	)

	globalFormatResult.mu.Lock()
	defer globalFormatResult.mu.Unlock()

	if !globalFormatResult.finished {
		return fmt.Errorf("FormatEx did not invoke the completion callback")
	}
	if !globalFormatResult.success {
		return fmifsErrorFromCommand(globalFormatResult.lastErr)
	}
	return nil
}

func fmifsErrorFromCommand(cmd fmifsCallbackCommand) error {
	switch cmd {
	case fmifsProgress:
		return fmt.Errorf("format failed (volume may be locked or in use)")
	case fmifsIncompatibleFileSystem:
		return fmt.Errorf("incompatible filesystem for this volume")
	case fmifsAccessDenied:
		return fmt.Errorf("access denied (is the volume locked by another process?)")
	case fmifsMediaWriteProtected:
		return fmt.Errorf("media is write-protected")
	case fmifsVolumeInUse:
		return fmt.Errorf("volume is in use")
	default:
		return fmt.Errorf("format failed (callback command 0x%02X)", uint32(cmd))
	}
}

// ---------------------------------------------------------------------------
// Option B: VDS COM API (fallback)
// ---------------------------------------------------------------------------

// VDS COM GUIDs and IIDs. These are defined by the Windows SDK and do not
// change across Windows versions.
var (
	clsidVdsLoader      = ole.NewGUID("{9C38ED61-D3BF-4AE4-A717-4D2FF8F81B10}")
	iidVdsServiceLoader = ole.NewGUID("{E0393303-90D4-4A97-AB71-E9B671EE2729}")
	iidVdsSwProvider    = ole.NewGUID("{9AA58360-CE33-4F92-B658-ED24B14425B8}")
	iidVdsPack          = ole.NewGUID("{3B69D7F5-9D94-4648-91CA-79939BA263BF}")
	iidVdsVolumeMF      = ole.NewGUID("{EE2D5DED-6236-4169-931D-B9778CE03DC6}")
)

// VDS query provider mask.
const vdsQuerySoftwareProviders = 0x1

// comObject wraps a raw COM interface pointer for VDS operations. We avoid
// go-ole's IUnknown/IDispatch types for VDS interfaces because their vtable
// layouts don't match IDispatch, and go-ole's QueryInterface returns
// *IDispatch which assumes a 7-slot vtable. Instead we manage the raw
// pointers and call AddRef/Release via the universal IUnknown vtable slots.
//
// The pointer is stored as unsafe.Pointer (not uintptr) so that the garbage
// collector treats it as a live reference, and to satisfy go vet's pointer
// conversion rules.
type comObject struct {
	ptr unsafe.Pointer // raw COM interface pointer
}

func newCOMObject(ptr uintptr) *comObject {
	// This conversion from uintptr to unsafe.Pointer is safe because the
	// uintptr comes directly from a syscall return or COM output parameter
	// within the same expression.
	return &comObject{ptr: unsafe.Pointer(ptr)} //nolint:govet
}

// vtable returns the vtable pointer (the first pointer-sized field in any
// COM object), cast to a slice-accessible form.
func (c *comObject) vtable() *[1024]uintptr {
	return *(**[1024]uintptr)(c.ptr)
}

// method returns the function pointer at the given vtable index.
func (c *comObject) method(index int) uintptr {
	return c.vtable()[index]
}

// uptr returns the pointer as uintptr for syscall arguments.
func (c *comObject) uptr() uintptr {
	return uintptr(c.ptr)
}

// release calls IUnknown::Release (vtable index 2).
func (c *comObject) release() {
	if c.ptr != nil {
		syscall.SyscallN(c.method(2), c.uptr())
		c.ptr = nil
	}
}

// queryInterface calls IUnknown::QueryInterface (vtable index 0).
func (c *comObject) queryInterface(iid *ole.GUID) (*comObject, error) {
	var out uintptr
	hr, _, _ := syscall.SyscallN(
		c.method(0),
		c.uptr(),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hr != 0 {
		return nil, fmt.Errorf("QueryInterface: HRESULT 0x%08X", hr)
	}
	return newCOMObject(out), nil
}

// formatViaVDS performs a full VDS COM enumeration to find and format the
// target volume. This is the heavyweight fallback.
func formatViaVDS(opts FormatVolumeOptions) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// S_FALSE (1) means COM was already initialized on this thread.
		var oleErr *ole.OleError
		if errors.As(err, &oleErr) && oleErr.Code() == 1 {
			// Already initialized -- not an error.
		} else {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()

	// Step 1: Create VDS loader (get IVdsServiceLoader).
	loaderOle, err := ole.CreateInstance(clsidVdsLoader, iidVdsServiceLoader)
	if err != nil {
		return fmt.Errorf("create VdsLoader: %w", err)
	}
	loader := &comObject{ptr: unsafe.Pointer(loaderOle)}
	defer loader.release()

	// Step 2: IVdsServiceLoader::LoadService(NULL) -> IVdsService.
	// Vtable index 3 (after IUnknown's 3 methods).
	var servicePtr uintptr
	hr, _, _ := syscall.SyscallN(
		loader.method(3),
		loader.uptr(),
		0, // NULL = local machine
		uintptr(unsafe.Pointer(&servicePtr)),
	)
	if hr != 0 {
		return fmt.Errorf("IVdsServiceLoader::LoadService: HRESULT 0x%08X", hr)
	}
	service := newCOMObject(servicePtr)
	defer service.release()

	// Step 3: WaitForServiceReady (vtable index 4 on IVdsService).
	if err := vdsWaitForServiceReady(service); err != nil {
		return fmt.Errorf("VDS service not ready: %w", err)
	}

	// Step 4: QueryProviders(VDS_QUERY_SOFTWARE_PROVIDERS) -> IEnumVdsObject.
	// IVdsService vtable: [0-2] IUnknown, [3] IsServiceReady,
	// [4] WaitForServiceReady, [5] GetProperties, [6] QueryProviders.
	var enumPtr uintptr
	hr, _, _ = syscall.SyscallN(
		service.method(6),
		service.uptr(),
		uintptr(vdsQuerySoftwareProviders),
		uintptr(unsafe.Pointer(&enumPtr)),
	)
	if hr != 0 {
		return fmt.Errorf("IVdsService::QueryProviders: HRESULT 0x%08X", hr)
	}
	enumProviders := newCOMObject(enumPtr)
	defer enumProviders.release()

	// Step 5-11: Walk providers -> packs -> volumes -> find by path -> format.
	return vdsEnumerateAndFormat(enumProviders, opts)
}

// vdsWaitForServiceReady polls IVdsService::WaitForServiceReady until the
// service is ready or a timeout occurs.
func vdsWaitForServiceReady(service *comObject) error {
	// IVdsService::WaitForServiceReady is vtable index 4.
	for i := 0; i < 100; i++ {
		hr, _, _ := syscall.SyscallN(service.method(4), service.uptr())
		if hr == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for VDS service (10 seconds)")
}

// vdsEnumerateAndFormat walks the VDS provider/pack/volume hierarchy to find
// the volume matching opts.VolumePath and formats it.
func vdsEnumerateAndFormat(enumProviders *comObject, opts FormatVolumeOptions) error {
	return vdsForEachObject(enumProviders, func(provUnk *comObject) error {
		// QI for IVdsSwProvider.
		swProvider, err := provUnk.queryInterface(iidVdsSwProvider)
		if err != nil {
			return nil // skip non-software providers
		}
		defer swProvider.release()

		// IVdsSwProvider::QueryPacks is vtable index 3.
		var enumPacksPtr uintptr
		hr, _, _ := syscall.SyscallN(
			swProvider.method(3),
			swProvider.uptr(),
			uintptr(unsafe.Pointer(&enumPacksPtr)),
		)
		if hr != 0 {
			return nil // skip this provider
		}
		enumPacks := newCOMObject(enumPacksPtr)
		defer enumPacks.release()

		return vdsForEachObject(enumPacks, func(packUnk *comObject) error {
			pack, err := packUnk.queryInterface(iidVdsPack)
			if err != nil {
				return nil
			}
			defer pack.release()

			// IVdsPack vtable: [0-2] IUnknown, [3] GetProperties,
			// [4] GetProvider, [5] QueryVolumes, [6] QueryDisks.
			var enumVolsPtr uintptr
			hr, _, _ := syscall.SyscallN(
				pack.method(5),
				pack.uptr(),
				uintptr(unsafe.Pointer(&enumVolsPtr)),
			)
			if hr != 0 {
				return nil
			}
			enumVols := newCOMObject(enumVolsPtr)
			defer enumVols.release()

			return vdsForEachObject(enumVols, func(volUnk *comObject) error {
				return vdsTryFormatVolume(volUnk, opts)
			})
		})
	})
}

// errVolumeFormatted is a sentinel used to short-circuit enumeration after a
// successful format.
var errVolumeFormatted = errors.New("volume formatted successfully")

func vdsTryFormatVolume(volUnk *comObject, opts FormatVolumeOptions) error {
	// QI for IVdsVolumeMF to check if this volume supports formatting.
	volMF, err := volUnk.queryInterface(iidVdsVolumeMF)
	if err != nil {
		return nil // not a mountable volume
	}
	defer volMF.release()

	// IVdsVolumeMF vtable: [0-2] IUnknown, [3] GetFileSystemProperties,
	// [4] Format, [5] AddAccessPath, [6] QueryAccessPaths,
	// [7] QueryReparsePoints, [8] DeleteAccessPath,
	// [9] Mount, [10] Dismount.
	//
	// QueryAccessPaths is at vtable index 6.
	var pathsArray uintptr
	var pathCount uint32
	hr, _, _ := syscall.SyscallN(
		volMF.method(6),
		volMF.uptr(),
		uintptr(unsafe.Pointer(&pathsArray)),
		uintptr(unsafe.Pointer(&pathCount)),
	)
	if hr != 0 || pathCount == 0 {
		return nil
	}

	// pathsArray is a COM-allocated array of LPWSTR pointers.
	targetPath := strings.ToUpper(strings.TrimRight(opts.VolumePath, `\`))
	found := false
	ptrs := (*[256]uintptr)(unsafe.Pointer(pathsArray))[:pathCount:pathCount]
	for _, ptr := range ptrs {
		if ptr == 0 {
			continue
		}
		path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
		if strings.ToUpper(strings.TrimRight(path, `\`)) == targetPath {
			found = true
		}
		ole.CoTaskMemFree(ptr)
	}
	ole.CoTaskMemFree(pathsArray)

	if !found {
		return nil
	}

	// Found the target volume. Format it.
	return vdsFormatVolumeMF(volMF, opts)
}

// vdsFormatVolumeMF calls IVdsVolumeMF::Format on the matched volume.
// IVdsVolumeMF::Format is vtable index 4:
//
//	HRESULT Format(
//	    VDS_FILE_SYSTEM_TYPE type,
//	    LPWSTR              pwszLabel,
//	    DWORD               dwUnitAllocationSize,
//	    BOOL                bForce,
//	    BOOL                bQuickFormat,
//	    BOOL                bEnableCompression,
//	    IVdsAsync**         ppAsync
//	);
func vdsFormatVolumeMF(volMF *comObject, opts FormatVolumeOptions) error {
	// VDS_FILE_SYSTEM_TYPE: VDS_FST_NTFS = 2, VDS_FST_EXFAT = 7.
	var fsType uint32
	switch strings.ToUpper(opts.FileSystem) {
	case "NTFS":
		fsType = 2 // VDS_FST_NTFS
	case "EXFAT":
		fsType = 7 // VDS_FST_EXFAT
	default:
		return fmt.Errorf("unsupported VDS filesystem: %s", opts.FileSystem)
	}

	label := opts.Label
	if label == "" {
		label = "USB"
	}
	labelPtr, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return fmt.Errorf("invalid volume label: %w", err)
	}

	quickFormat := uintptr(0)
	if opts.QuickFormat {
		quickFormat = 1
	}

	var asyncPtr uintptr
	hr, _, _ := syscall.SyscallN(
		volMF.method(4),
		volMF.uptr(),
		uintptr(fsType),
		uintptr(unsafe.Pointer(labelPtr)),
		uintptr(opts.ClusterSize),
		1, // bForce = TRUE
		quickFormat,
		0, // bEnableCompression = FALSE
		uintptr(unsafe.Pointer(&asyncPtr)),
	)
	if hr != 0 {
		return fmt.Errorf("IVdsVolumeMF::Format: HRESULT 0x%08X", hr)
	}

	if asyncPtr == 0 {
		return fmt.Errorf("IVdsVolumeMF::Format returned NULL async object")
	}
	asyncObj := newCOMObject(asyncPtr)
	defer asyncObj.release()

	if err := vdsWaitAsync(asyncObj); err != nil {
		return err
	}
	return errVolumeFormatted
}

// vdsWaitAsync calls IVdsAsync::Wait to block until the operation completes.
// IVdsAsync vtable: [0-2] IUnknown, [3] Cancel, [4] Wait, [5] QueryStatus.
func vdsWaitAsync(async *comObject) error {
	var hrResult int32
	// VDS_ASYNC_OUTPUT is a union; 64 bytes is a conservative upper bound.
	var asyncOutput [64]byte
	hr, _, _ := syscall.SyscallN(
		async.method(4),
		async.uptr(),
		uintptr(unsafe.Pointer(&hrResult)),
		uintptr(unsafe.Pointer(&asyncOutput[0])),
	)
	if hr != 0 {
		return fmt.Errorf("IVdsAsync::Wait: HRESULT 0x%08X", hr)
	}
	if hrResult < 0 {
		return fmt.Errorf("VDS async operation failed: HRESULT 0x%08X", uint32(hrResult))
	}
	return nil
}

// ---------------------------------------------------------------------------
// VDS enumeration helper
// ---------------------------------------------------------------------------

// vdsForEachObject calls fn for each object in an IEnumVdsObject. If fn
// returns errVolumeFormatted, enumeration stops and nil is returned. Any
// other non-nil error is propagated.
//
// IEnumVdsObject vtable: [0-2] IUnknown, [3] Next, [4] Skip, [5] Reset,
// [6] Clone.
func vdsForEachObject(enum *comObject, fn func(*comObject) error) error {
	for {
		var obj uintptr
		var fetched uint32
		hr, _, _ := syscall.SyscallN(
			enum.method(3), // Next
			enum.uptr(),
			1, // request one object at a time
			uintptr(unsafe.Pointer(&obj)),
			uintptr(unsafe.Pointer(&fetched)),
		)
		if hr != 0 || fetched == 0 {
			break // S_FALSE or error = no more objects
		}
		unk := newCOMObject(obj)
		err := fn(unk)
		unk.release()
		if errors.Is(err, errVolumeFormatted) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Volume discovery and mount point assignment
// ---------------------------------------------------------------------------

var (
	kernel32                          = windows.NewLazySystemDLL("kernel32.dll")
	procFindFirstVolumeW              = kernel32.NewProc("FindFirstVolumeW")
	procFindNextVolumeW               = kernel32.NewProc("FindNextVolumeW")
	procFindVolumeClose               = kernel32.NewProc("FindVolumeClose")
	procGetVolumePathNamesForVolumeNameW = kernel32.NewProc("GetVolumePathNamesForVolumeNameW")
	procSetVolumeMountPointW          = kernel32.NewProc("SetVolumeMountPointW")
	procDeleteVolumeMountPointW       = kernel32.NewProc("DeleteVolumeMountPointW")
)

// IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS returns the physical disk(s) a volume
// spans. We use it to map volumes to physical disk numbers.
const ioctlVolumeGetVolumeDiskExtents = 0x00560000

// rawDiskExtent maps to DISK_EXTENT.
type rawDiskExtent struct {
	DiskNumber     uint32
	_              uint32 // padding
	StartingOffset int64
	ExtentLength   int64
}

// rawVolumeDiskExtents maps to VOLUME_DISK_EXTENTS.
type rawVolumeDiskExtents struct {
	NumberOfDiskExtents uint32
	_                   uint32 // padding
	Extents             [1]rawDiskExtent
}

// FindVolumeByDiskNumber enumerates all volumes on the system and returns
// the volume GUID path (e.g. `\\?\Volume{GUID}\`) for the first volume
// found on the given physical disk number. Returns an error if no volume
// is found.
func FindVolumeByDiskNumber(diskNumber int) (string, error) {
	buf := make([]uint16, 260)

	hFind, _, err := procFindFirstVolumeW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	handle := windows.Handle(hFind)
	if handle == windows.InvalidHandle {
		return "", fmt.Errorf("FindFirstVolumeW: %w", err)
	}
	defer procFindVolumeClose.Call(hFind)

	for {
		volumePath := windows.UTF16ToString(buf)
		if matchesPhysicalDisk(volumePath, diskNumber) {
			return volumePath, nil
		}

		r, _, err := procFindNextVolumeW.Call(
			hFind,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
		)
		if r == 0 {
			// ERROR_NO_MORE_FILES (18) is expected at the end of enumeration.
			if errno, ok := err.(syscall.Errno); ok && errno == 18 {
				break
			}
			return "", fmt.Errorf("FindNextVolumeW: %w", err)
		}
	}

	return "", fmt.Errorf("no volume found on PhysicalDrive%d", diskNumber)
}

// FindAllVolumesByDiskNumber enumerates all volumes on the system and returns
// every volume GUID path (e.g. `\\?\Volume{GUID}\`) residing on the given
// physical disk number. Returns nil (not an error) if no volumes are found.
func FindAllVolumesByDiskNumber(diskNumber int) []string {
	buf := make([]uint16, 260)

	hFind, _, err := procFindFirstVolumeW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	handle := windows.Handle(hFind)
	if handle == windows.InvalidHandle {
		_ = err
		return nil
	}
	defer procFindVolumeClose.Call(hFind)

	var volumes []string
	for {
		volumePath := windows.UTF16ToString(buf)
		if matchesPhysicalDisk(volumePath, diskNumber) {
			volumes = append(volumes, volumePath)
		}

		r, _, err := procFindNextVolumeW.Call(
			hFind,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
		)
		if r == 0 {
			if errno, ok := err.(syscall.Errno); ok && errno == 18 {
				break
			}
			break
		}
	}

	return volumes
}

// matchesPhysicalDisk checks whether a volume GUID path resides on the given
// physical disk by querying its disk extents.
func matchesPhysicalDisk(volumeGUIDPath string, diskNumber int) bool {
	// Remove the trailing backslash to open the volume device.
	devPath := strings.TrimRight(volumeGUIDPath, `\`)
	pathPtr, err := syscall.UTF16PtrFromString(devPath)
	if err != nil {
		return false
	}

	h, err := windows.CreateFile(
		pathPtr,
		0, // no access needed for this IOCTL
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var extents rawVolumeDiskExtents
	var bytesReturned uint32
	err = windows.DeviceIoControl(
		h,
		ioctlVolumeGetVolumeDiskExtents,
		nil, 0,
		(*byte)(unsafe.Pointer(&extents)),
		uint32(unsafe.Sizeof(extents)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return false
	}

	return extents.NumberOfDiskExtents > 0 &&
		int(extents.Extents[0].DiskNumber) == diskNumber
}

// GetVolumeDriveLetter returns the drive letter (e.g. "E:\") assigned to a
// volume GUID path, or an empty string if none is assigned.
func GetVolumeDriveLetter(volumeGUIDPath string) (string, error) {
	volPtr, err := syscall.UTF16PtrFromString(volumeGUIDPath)
	if err != nil {
		return "", fmt.Errorf("invalid volume path: %w", err)
	}

	buf := make([]uint16, 260)
	var returnLength uint32

	r, _, callErr := procGetVolumePathNamesForVolumeNameW.Call(
		uintptr(unsafe.Pointer(volPtr)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&returnLength)),
	)
	if r == 0 {
		return "", fmt.Errorf("GetVolumePathNamesForVolumeNameW: %w", callErr)
	}

	// The buffer contains a multi-string (double-null terminated). The first
	// entry is the mount point, typically "E:\".
	path := windows.UTF16ToString(buf)
	if len(path) >= 2 && path[1] == ':' {
		return path, nil
	}
	return "", nil
}

// AssignDriveLetter assigns an available drive letter to the given volume
// GUID path. It scans from Z: down to D: to find a free letter.
// Returns the assigned path (e.g. "G:\").
func AssignDriveLetter(volumeGUIDPath string) (string, error) {
	// Ensure volume path ends with backslash.
	if !strings.HasSuffix(volumeGUIDPath, `\`) {
		volumeGUIDPath += `\`
	}

	volPtr, err := syscall.UTF16PtrFromString(volumeGUIDPath)
	if err != nil {
		return "", fmt.Errorf("invalid volume path: %w", err)
	}

	// Try letters from Z down to D (skip A/B floppy, C system).
	for letter := byte('Z'); letter >= 'D'; letter-- {
		mountPoint := string(letter) + `:\`
		mountPtr, _ := syscall.UTF16PtrFromString(mountPoint)

		// Check if this letter is already in use by trying to query it.
		testPath := string(letter) + `:\`
		testPtr, _ := syscall.UTF16PtrFromString(testPath)
		attrs, _ := windows.GetFileAttributes(testPtr)
		if attrs != windows.INVALID_FILE_ATTRIBUTES {
			continue // letter is in use
		}

		r, _, callErr := procSetVolumeMountPointW.Call(
			uintptr(unsafe.Pointer(mountPtr)),
			uintptr(unsafe.Pointer(volPtr)),
		)
		if r != 0 {
			return testPath, nil
		}
		// If SetVolumeMountPoint fails (e.g., letter taken by another volume
		// that has no files), try the next letter.
		_ = callErr
	}

	return "", fmt.Errorf("no available drive letter found")
}

// RemoveDriveLetter removes the mount point (drive letter) from a path like
// "E:\". This is useful before ejecting or reformatting.
func RemoveDriveLetter(mountPoint string) error {
	if !strings.HasSuffix(mountPoint, `\`) {
		mountPoint += `\`
	}
	ptr, err := syscall.UTF16PtrFromString(mountPoint)
	if err != nil {
		return fmt.Errorf("invalid mount point: %w", err)
	}

	r, _, callErr := procDeleteVolumeMountPointW.Call(
		uintptr(unsafe.Pointer(ptr)),
	)
	if r == 0 {
		return fmt.Errorf("DeleteVolumeMountPointW(%s): %w", mountPoint, callErr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Post-partition helpers
// ---------------------------------------------------------------------------

// WaitForVolumeReady waits up to the given duration for Windows to recognize
// a new volume on the specified disk after partition creation. It calls
// IOCTL_DISK_UPDATE_PROPERTIES on the disk handle and then polls for the
// volume to appear.
//
// The disk handle must already be open. Returns the volume GUID path once
// found, or an error on timeout.
func WaitForVolumeReady(diskHandle windows.Handle, diskNumber int, timeout time.Duration) (string, error) {
	// Tell the kernel to re-read the partition table.
	_ = UpdateDiskProperties(diskHandle)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		volumePath, err := FindVolumeByDiskNumber(diskNumber)
		if err == nil && volumePath != "" {
			return volumePath, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return "", fmt.Errorf("timed out waiting for volume on PhysicalDrive%d after %v", diskNumber, timeout)
}
