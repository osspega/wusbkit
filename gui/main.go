package main

import (
	"context"
	"embed"

	"github.com/lazaroagomez/wusbkit/gui/services"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	deviceService := services.NewDeviceService()
	flashService := services.NewFlashService()
	formatService := services.NewFormatService()
	diskService := services.NewDiskService()
	imageService := services.NewImageService()

	err := wails.Run(&options.App{
		Title:            "wusbkit",
		Width:            960,
		Height:           640,
		MinWidth:         800,
		MinHeight:        500,
		BackgroundColour: &options.RGBA{R: 32, G: 32, B: 32, A: 255},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			deviceService.SetContext(ctx)
			flashService.SetContext(ctx)
			formatService.SetContext(ctx)
			diskService.SetContext(ctx)
			imageService.SetContext(ctx)
		},
		Bind: []interface{}{
			app,
			deviceService,
			flashService,
			formatService,
			diskService,
			imageService,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.SystemDefault,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
