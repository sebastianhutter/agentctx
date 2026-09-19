package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"fyne.io/fyne/v2"
)

// trayIcon draws the menu bar icon: a miniature of the app icon — a rounded
// slate square holding three log entries of decreasing length, the last one
// accented.
//
// Filled rather than monochrome, which is a deliberate departure from the usual
// macOS menu bar convention. A template icon is the native choice and macOS
// inverts it per appearance, but Fyne only requests one for a themed resource,
// and a plain dark glyph is invisible against a dark menu bar. A filled badge
// carries its own contrast: light bars read against the dark square whatever is
// behind it, and the square reads against a light menu bar.
//
// Drawn at 64px so it stays sharp when macOS scales it to the menu bar height
// on a retina display.
func trayIcon() fyne.Resource {
	const size = 64

	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Same palette as Icon.png, so the two read as one thing.
	top := color.RGBA{34, 43, 58, 255}
	bottom := color.RGBA{19, 25, 36, 255}
	ink := color.RGBA{236, 240, 246, 255}
	accent := color.RGBA{122, 190, 255, 255}

	const radius = 14.0
	const inset = 1.0

	for y := range size {
		t := float64(y) / float64(size-1)
		row := color.RGBA{
			R: uint8(float64(top.R) + t*(float64(bottom.R)-float64(top.R))),
			G: uint8(float64(top.G) + t*(float64(bottom.G)-float64(top.G))),
			B: uint8(float64(top.B) + t*(float64(bottom.B)-float64(top.B))),
			A: 255,
		}
		for x := range size {
			if a := roundedCoverage(float64(x), float64(y), inset, inset,
				size-2*inset, size-2*inset, radius); a > 0 {
				c := row
				c.A = uint8(a * 255)
				img.Set(x, y, over(img.RGBAAt(x, y), c))
			}
		}
	}

	// Three entries, shortening, the newest accented — and a dot each, because
	// one entry is one file.
	bars := []struct {
		y, w float64
		c    color.RGBA
	}{
		{y: 21, w: 26, c: ink},
		{y: 33, w: 19, c: ink},
		{y: 45, w: 12, c: accent},
	}
	const x0 = 12.0
	const thickness = 6.0

	for _, b := range bars {
		drawRounded(img, x0, b.y-thickness/2, b.w, thickness, thickness/2, b.c)
		drawRounded(img, x0+b.w+6, b.y-thickness/2, thickness, thickness, thickness/2, b.c)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return fyne.NewStaticResource("agentctx.png", buf.Bytes())
}

// roundedCoverage returns how much of the pixel at (x, y) falls inside a
// rounded rectangle, antialiasing the corners.
func roundedCoverage(x, y, rx, ry, w, h, r float64) float64 {
	cx := math.Min(math.Max(x+0.5, rx+r), rx+w-r)
	cy := math.Min(math.Max(y+0.5, ry+r), ry+h-r)
	d := math.Hypot(x+0.5-cx, y+0.5-cy)
	switch {
	case d <= r-0.7:
		return 1
	case d >= r+0.7:
		return 0
	default:
		return (r + 0.7 - d) / 1.4
	}
}

// drawRounded fills a rounded rectangle.
func drawRounded(img *image.RGBA, x, y, w, h, r float64, c color.RGBA) {
	bounds := img.Bounds()
	for py := int(y) - 2; py <= int(y+h)+2; py++ {
		for px := int(x) - 2; px <= int(x+w)+2; px++ {
			if px < bounds.Min.X || py < bounds.Min.Y || px >= bounds.Max.X || py >= bounds.Max.Y {
				continue
			}
			a := roundedCoverage(float64(px), float64(py), x, y, w, h, r)
			if a <= 0 {
				continue
			}
			cc := c
			cc.A = uint8(a * 255)
			img.Set(px, py, over(img.RGBAAt(px, py), cc))
		}
	}
}

// over composites src onto dst.
func over(dst, src color.RGBA) color.RGBA {
	sa := float64(src.A) / 255
	da := float64(dst.A) / 255
	outA := sa + da*(1-sa)
	if outA == 0 {
		return color.RGBA{}
	}
	mix := func(s, d uint8) uint8 {
		return uint8((float64(s)/255*sa + float64(d)/255*da*(1-sa)) / outA * 255)
	}
	return color.RGBA{mix(src.R, dst.R), mix(src.G, dst.G), mix(src.B, dst.B), uint8(outA * 255)}
}
