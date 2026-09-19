package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"fyne.io/fyne/v2"
)

// trayIcon draws the menu bar icon: three log entries of decreasing length,
// the same motif as the app icon.
//
// Template-style, in black with transparency, because macOS tints menu bar
// icons itself and a coloured one looks wrong in dark mode.
func trayIcon() fyne.Resource {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	ink := color.RGBA{R: 0, G: 0, B: 0, A: 0xFF}

	// Three bars, decreasing, each with a dot: an append-only log.
	bars := []struct{ y, w int }{
		{y: 8, w: 15},
		{y: 15, w: 11},
		{y: 22, w: 7},
	}
	const x0 = 4
	const thickness = 3

	for _, b := range bars {
		fillRect(img, ink, x0, b.y, x0+b.w, b.y+thickness)
		dotX := x0 + b.w + 4
		fillRect(img, ink, dotX, b.y, dotX+thickness, b.y+thickness)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return fyne.NewStaticResource("icon.png", buf.Bytes())
}

// fillRect fills a rectangle on the image.
func fillRect(img *image.RGBA, c color.RGBA, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}
