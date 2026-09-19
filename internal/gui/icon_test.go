package gui

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestTrayIconIsVisibleOnAnyBackground(t *testing.T) {
	res := trayIcon()

	img, err := png.Decode(bytes.NewReader(res.Content()))
	if err != nil {
		t.Fatalf("tray icon is not a valid PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Errorf("icon is %dx%d, want 64x64 so it stays sharp when scaled", b.Dx(), b.Dy())
	}

	// The failure this replaces: a dark glyph on a dark menu bar. The icon must
	// carry its own contrast, so it needs both a mostly opaque body and pixels
	// bright enough to read against it.
	var opaque, bright int
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0x8000 {
				opaque++
			}
			if a > 0x8000 && (r+g+b)/3 > 0x9000 {
				bright++
			}
		}
	}
	total := bounds.Dx() * bounds.Dy()
	if opaque < total/2 {
		t.Errorf("only %d of %d pixels are opaque; a filled badge is what makes it visible", opaque, total)
	}
	if bright < 100 {
		t.Errorf("only %d bright pixels; the glyph would not read against the dark square", bright)
	}

	if out := os.Getenv("AGENTCTX_ICON_PREVIEW"); out != "" {
		if err := os.WriteFile(out, res.Content(), 0o644); err != nil {
			t.Fatalf("write preview: %v", err)
		}
		t.Logf("wrote preview to %s", out)
	}
}
