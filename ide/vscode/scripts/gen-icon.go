//go:build ignore

// Command gen-icon renders the VS Code extension icon.
//
// The icon is generated rather than hand-drawn so it stays consistent with
// activity-bar.svg and can be rebuilt after any change to it. Rasterizing an
// SVG would need a rendering dependency; the icon is five line segments, so
// drawing them directly is smaller and has no third-party code in the build.
//
// Usage:
//
//	go run ide/vscode/scripts/gen-icon.go
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

const (
	size = 128

	// The same glyph as activity-bar.svg: a flat line, a dip, a tall peak, a
	// second dip, then flat again.
	//
	// Coordinates are given in a 24-unit grid and scaled up, matching the SVG's
	// viewBox so the two stay in step.
	grid    = 24.0
	padding = 3.0 // grid units of inset on each side

	// Brand green, matching the "good" band in the score palette. Kept as plain
	// component constants rather than a color.RGBA because struct fields cannot be
	// const in Go.
	strokeR = 0x3f
	strokeG = 0xb9
	strokeB = 0x50
)

// Points of the pulse polyline, in the 24-unit grid.
var polyline = [][2]float64{
	{3, 12},
	{6, 12},
	{8.5, 5},
	{11.5, 19},
	{14, 12},
	{21, 12},
}

func main() {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// Transparent background. A marketplace icon sits on whatever the user's
	// theme provides, so an opaque square would show as a visible block.
	for y := range img.Pix {
		img.Pix[y] = 0
	}

	scale := float64(size) / grid
	off := padding * scale
	span := float64(size) - 2*off

	toPx := func(g float64) float64 { return off + (g-padding)/(grid-2*padding)*span }
	// Stroke width scales with the canvas so the icon reads the same whether it
	// is rendered at 16px in the activity bar or 128px on the marketplace.
	w := max(2, int(2.1*scale/6*3))

	for i := 0; i < len(polyline)-1; i++ {
		drawLine(img, toPx(polyline[i][0]), toPx(polyline[i][1]),
			toPx(polyline[i+1][0]), toPx(polyline[i+1][1]), w)
	}

	out := filepath.Join("ide", "vscode", "media", "icon.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fail(err)
	}
	f, err := os.Create(out)
	if err != nil {
		fail(err)
	}
	defer f.Close()

	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (%dx%d)\n", out, size, size)
}

// drawLine rasterizes a line segment as a filled circle swept along its length.
//
// A distance test per pixel rather than Bresenham: at 128px the cost is
// irrelevant, and the round caps and joins that a Bresenham implementation would
// need to special-case come out correct for free.
func drawLine(img *image.RGBA, x0, y0, x1, y1 float64, width int) {
	radius := float64(width) / 2
	minX := max(0, int(min(x0, x1)-radius-1))
	maxX := min(size-1, int(max(x0, x1)+radius+1))
	minY := max(0, int(min(y0, y1)-radius-1))
	maxY := min(size-1, int(max(y0, y1)+radius+1))

	dx, dy := x1-x0, y1-y0
	lenSq := dx*dx + dy*dy

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5

			// Projection of the pixel centre onto the segment, clamped to the
			// endpoints so the caps stay round.
			var t float64
			if lenSq > 0 {
				t = ((px-x0)*dx + (py-y0)*dy) / lenSq
				t = max(0, min(1, t))
			}
			nx, ny := x0+t*dx, y0+t*dy

			distSq := (px-nx)*(px-nx) + (py-ny)*(py-ny)
			if distSq > radius*radius {
				continue
			}

			// One pixel of antialiasing on the edge. Without it the diagonal
			// strokes look visibly jagged at marketplace sizes.
			alpha := 255.0
			if edge := radius - sqrt(distSq); edge < 1 {
				alpha = 255 * max(0, edge)
			}
			blend(img, x, y, strokeR, strokeG, strokeB, alpha)
		}
	}
}

// blend composites c over the existing pixel, so overlapping strokes do not
// compound their own alpha.
func blend(img *image.RGBA, x, y int, r, g, b uint8, alpha float64) {
	if alpha <= 0 {
		return
	}
	i := img.PixOffset(x, y)
	a := min(1, alpha/255)
	dst := img.Pix[i : i+4]
	inv := 1 - a
	dst[0] = uint8(float64(dst[0])*inv + float64(r)*a)
	dst[1] = uint8(float64(dst[1])*inv + float64(g)*a)
	dst[2] = uint8(float64(dst[2])*inv + float64(b)*a)
	dst[3] = max(dst[3], uint8(alpha))
}

func sqrt(v float64) float64 {
	if v <= 0 {
		return 0
	}
	// Newton's method. Only ever used for values in the 0..radius squared range,
	// so a handful of iterations is exact to well past a pixel.
	x := v
	for range 12 {
		x = 0.5 * (x + v/x)
	}
	return x
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-icon:", err)
	os.Exit(1)
}
