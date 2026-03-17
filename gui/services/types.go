package services

// DeviceDTO is the frontend-friendly representation of a USB device.
type DeviceDTO struct {
	DiskNumber     int    `json:"diskNumber"`
	DriveLetter    string `json:"driveLetter"`
	FriendlyName   string `json:"friendlyName"`
	Model          string `json:"model"`
	SerialNumber   string `json:"serialNumber"`
	Size           int64  `json:"size"`
	SizeHuman      string `json:"sizeHuman"`
	FileSystem     string `json:"fileSystem"`
	VolumeLabel    string `json:"volumeLabel"`
	PartitionStyle string `json:"partitionStyle"`
	Status         string `json:"status"`
	HealthStatus   string `json:"healthStatus"`
	BusType        string `json:"busType"`
	LocationInfo   string `json:"locationInfo"`
}

// FlashOptionsDTO holds flash configuration from the frontend.
type FlashOptionsDTO struct {
	DiskNumbers   []int  `json:"diskNumbers"`
	ImagePath     string `json:"imagePath"`
	Verify        bool   `json:"verify"`
	CalculateHash bool   `json:"calculateHash"`
	SkipUnchanged bool   `json:"skipUnchanged"`
	BufferSizeMB  int    `json:"bufferSizeMB"`
	Extract       bool   `json:"extract"`
}

// FormatOptionsDTO holds format configuration from the frontend.
type FormatOptionsDTO struct {
	DiskNumbers []int  `json:"diskNumbers"`
	FileSystem  string `json:"fileSystem"`
	Label       string `json:"label"`
	Quick       bool   `json:"quick"`
}

// LabelOptionsDTO holds label configuration from the frontend.
type LabelOptionsDTO struct {
	DiskNumbers []int  `json:"diskNumbers"`
	Label       string `json:"label"`
}

// CreateImageOptionsDTO holds image creation config from the frontend.
type CreateImageOptionsDTO struct {
	DiskNumber int    `json:"diskNumber"`
	OutputPath string `json:"outputPath"`
	Verify     bool   `json:"verify"`
}

// FlashProgressDTO is emitted as a Wails event during flash operations.
type FlashProgressDTO struct {
	DiskNumber   int    `json:"diskNumber"`
	Stage        string `json:"stage"`
	Percentage   int    `json:"percentage"`
	BytesWritten int64  `json:"bytesWritten"`
	TotalBytes   int64  `json:"totalBytes"`
	Speed        string `json:"speed"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	Hash         string `json:"hash,omitempty"`
	BytesSkipped int64  `json:"bytesSkipped,omitempty"`
}

// FormatProgressDTO is emitted as a Wails event during format operations.
type FormatProgressDTO struct {
	DiskNumber  int    `json:"diskNumber"`
	DriveLetter string `json:"driveLetter,omitempty"`
	Stage       string `json:"stage"`
	Percentage  int    `json:"percentage"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

// ImageProgressDTO is emitted as a Wails event during image creation.
type ImageProgressDTO struct {
	DiskNumber int    `json:"diskNumber"`
	Stage      string `json:"stage"`
	Percentage int    `json:"percentage"`
	BytesRead  int64  `json:"bytesRead"`
	TotalBytes int64  `json:"totalBytes"`
	Speed      string `json:"speed"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}
