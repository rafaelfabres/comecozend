package main

import (
	"image"
	"os"

	qrcode "github.com/skip2/go-qrcode"

	"leaf-mlp1-poc/internal/sdlui"
)

// makeQR renders a QR code for the game's itch.io URL and uploads it as a
// texture, mirroring Leaf's detail screen ("Scan to open on itch.io"): the
// handheld can't usefully browse the page, but a phone can.
func makeQR(screen *sdlui.Screen, url string) int {
	if url == "" {
		return -1
	}
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return -1
	}
	code.DisableBorder = false
	img := code.Image(256)

	// go-qrcode returns a paletted/gray image; convert to the RGBA layout the
	// texture uploader expects.
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	slot := screen.SetQRCode(rgba.Pix, bounds.Dx(), bounds.Dy())
	if slot < 0 {
		os.Stderr.WriteString("could not upload QR texture\n")
	}
	return slot
}
