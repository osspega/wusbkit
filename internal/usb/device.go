package usb

import (
	"fmt"
	"regexp"
)

// Device represents a USB storage device
type Device struct {
	DriveLetter      string `json:"driveLetter"`
	DiskNumber       int    `json:"diskNumber"`
	FriendlyName     string `json:"friendlyName"`
	Model            string `json:"model"`
	Size             int64  `json:"size"`
	SizeHuman        string `json:"sizeHuman"`
	SerialNumber     string `json:"serialNumber"`
	VendorID         string `json:"vendorId"`
	ProductID        string `json:"productId"`
	FileSystem       string `json:"fileSystem"`
	VolumeLabel      string `json:"volumeLabel"`
	PartitionStyle   string `json:"partitionStyle"`
	Status           string `json:"status"`
	HealthStatus     string `json:"healthStatus"`
	BusType          string `json:"busType"`
	MediaType        string `json:"mediaType"`
	LocationInfo     string `json:"locationInfo"`     // USB hub port location (e.g., "Port_#0002.Hub_#0002")
	ParentInstanceId string `json:"parentInstanceId"` // Parent hub instance ID (e.g., "USB\VID_2109&PID_0822\...")
}

// FormatSize converts bytes to human-readable format
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// vidPidRegex matches VID_XXXX&PID_XXXX patterns in PNPDeviceID
var vidPidRegex = regexp.MustCompile(`VID_([0-9A-Fa-f]{4})&PID_([0-9A-Fa-f]{4})`)

// ParseVIDPID extracts VID and PID from a PNPDeviceID string
// Example: "USB\VID_0781&PID_5567\4C530001181205121531"
func ParseVIDPID(pnpDeviceID string) (vid, pid string) {
	matches := vidPidRegex.FindStringSubmatch(pnpDeviceID)
	if len(matches) == 3 {
		return matches[1], matches[2]
	}
	return "", ""
}

// rawDisk represents the raw disk data from Get-Disk queries.
type rawDisk struct {
	Number            int         `json:"Number"`
	FriendlyName      string      `json:"FriendlyName"`
	Model             string      `json:"Model"`
	SerialNumber      string      `json:"SerialNumber"`
	Size              int64       `json:"Size"`
	PartitionStyle    string      `json:"PartitionStyle"`
	HealthStatus      string      `json:"HealthStatus"`
	OperationalStatus interface{} `json:"OperationalStatus"`
	BusType           string      `json:"BusType"`
}

// rawPhysicalDisk represents the raw data from Get-PhysicalDisk queries.
type rawPhysicalDisk struct {
	DeviceId      string `json:"DeviceId"`
	FriendlyName  string `json:"FriendlyName"`
	SerialNumber  string `json:"SerialNumber"`
	MediaType     int    `json:"MediaType"`
	BusType       int    `json:"BusType"`
	HealthStatus  int    `json:"HealthStatus"`
}

// rawPartition represents the raw data from Get-Partition queries.
type rawPartition struct {
	DiskNumber      int    `json:"DiskNumber"`
	PartitionNumber int    `json:"PartitionNumber"`
	DriveLetter     string `json:"DriveLetter"`
	Size            int64  `json:"Size"`
}

// rawVolume represents the raw data from Get-Volume queries.
type rawVolume struct {
	DriveLetter     string `json:"DriveLetter"`
	FileSystemLabel string `json:"FileSystemLabel"`
	FileSystem      string `json:"FileSystem"`
	Size            int64  `json:"Size"`
	SizeRemaining   int64  `json:"SizeRemaining"`
	HealthStatus    int    `json:"HealthStatus"`
	DriveType       int    `json:"DriveType"`
}

// rawWin32DiskDrive represents the raw data from Win32_DiskDrive queries.
type rawWin32DiskDrive struct {
	DeviceID      string `json:"DeviceID"`
	Index         int    `json:"Index"`
	Model         string `json:"Model"`
	SerialNumber  string `json:"SerialNumber"`
	PNPDeviceID   string `json:"PNPDeviceID"`
	InterfaceType string `json:"InterfaceType"`
}

// partitionStyleNames maps partition style numbers to names
var partitionStyleNames = map[int]string{
	0: "RAW",
	1: "MBR",
	2: "GPT",
}

// healthStatusNames maps health status numbers to names
var healthStatusNames = map[int]string{
	0: "Healthy",
	1: "Warning",
	2: "Unhealthy",
	5: "Unknown",
}

// mediaTypeNames maps media type numbers to names
var mediaTypeNames = map[int]string{
	0: "Unspecified",
	3: "HDD",
	4: "SSD",
}

// getPartitionStyleName returns the partition style name for a numeric value
func getPartitionStyleName(style int) string {
	if name, ok := partitionStyleNames[style]; ok {
		return name
	}
	return "Unknown"
}

// getHealthStatusName returns the health status name for a numeric value
func getHealthStatusName(status int) string {
	if name, ok := healthStatusNames[status]; ok {
		return name
	}
	return "Unknown"
}

// getMediaTypeName returns the media type name for a numeric value
func getMediaTypeName(mediaType int) string {
	if name, ok := mediaTypeNames[mediaType]; ok {
		return name
	}
	return "Unknown"
}
