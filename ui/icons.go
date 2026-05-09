//go:build windows

package ui

import (
	"image"
	"image/color"
)

// Tray icons are generated programmatically so the binary stays self-contained
// (no external .ico files). The active glyph is a stylized stretching figure;
// the paused variant is desaturated and overlaid with two pause bars.

const iconSize = 32

func ActiveTrayIcon() image.Image  { return drawTrayIcon(false) }
func PausedTrayIcon() image.Image  { return drawTrayIcon(true) }

func drawTrayIcon(paused bool) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	fg := color.NRGBA{R: 0x4f, G: 0xc3, B: 0xf7, A: 0xff} // light blue
	if paused {
		fg = color.NRGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff} // gray
	}
	// Head.
	fillCircle(img, 16, 7, 3, fg)
	// Body.
	drawLine(img, 16, 10, 16, 22, fg, 2)
	// Arms — T-pose.
	drawLine(img, 6, 14, 26, 14, fg, 2)
	// Legs.
	drawLine(img, 16, 22, 10, 30, fg, 2)
	drawLine(img, 16, 22, 22, 30, fg, 2)

	if paused {
		bar := color.NRGBA{R: 0xff, G: 0x9b, B: 0x42, A: 0xff} // amber
		fillRect(img, 21, 20, 23, 29, bar)
		fillRect(img, 25, 20, 27, 29, bar)
	}
	return img
}

func fillCircle(img *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	r2 := r * r
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r2 {
				img.SetNRGBA(cx+x, cy+y, c)
			}
		}
	}
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawLine(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA, thickness int) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	t := thickness / 2
	for {
		for oy := -t; oy <= t; oy++ {
			for ox := -t; ox <= t; ox++ {
				img.SetNRGBA(x0+ox, y0+oy, c)
			}
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
