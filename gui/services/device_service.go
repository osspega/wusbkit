package services

import (
	"context"

	"github.com/lazaroagomez/wusbkit/internal/usb"
)

// DeviceService provides USB device enumeration for the frontend.
type DeviceService struct {
	ctx  context.Context
	enum *usb.Enumerator
}

func NewDeviceService() *DeviceService {
	return &DeviceService{
		enum: usb.NewEnumerator(),
	}
}

func (s *DeviceService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// ListDevices returns all connected USB devices.
func (s *DeviceService) ListDevices() ([]DeviceDTO, error) {
	devices, err := s.enum.ListDevices()
	if err != nil {
		return nil, err
	}

	dtos := make([]DeviceDTO, len(devices))
	for i, d := range devices {
		dtos[i] = DeviceDTO{
			DiskNumber:     d.DiskNumber,
			DriveLetter:    d.DriveLetter,
			FriendlyName:   d.FriendlyName,
			Model:          d.Model,
			SerialNumber:   d.SerialNumber,
			Size:           d.Size,
			SizeHuman:      d.SizeHuman,
			FileSystem:     d.FileSystem,
			VolumeLabel:    d.VolumeLabel,
			PartitionStyle: d.PartitionStyle,
			Status:         d.Status,
			HealthStatus:   d.HealthStatus,
			BusType:        d.BusType,
			LocationInfo:   d.LocationInfo,
		}
	}
	return dtos, nil
}
