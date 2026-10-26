package imgproc

import (
	"image"
	"image/color"
)

// TestPattern builds a calibration image in *printer orientation*
// (width = across the head, height = feed direction). It contains:
//
//   - a 1-dot border (checks edge clipping / printable width)
//   - ruler ticks along the left edge: 1 tick per 8 dots (1mm),
//     longer every 40 dots (5mm) — checks feed scaling
//   - diagonal lines (checks for dropped/duplicated rows)
//   - a grayscale gradient (checks dithering)
//   - solid black and white bars
//
// Print it with Rotate=0. If the border is clipped on one side, the
// effective printable width is smaller than the configured width.
func TestPattern(width, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	black := color.Gray{Y: 0}
	set := func(x, y int) {
		if x >= 0 && x < width && y >= 0 && y < height {
			img.SetGray(x, y, black)
		}
	}

	// Border.
	for x := 0; x < width; x++ {
		set(x, 0)
		set(x, height-1)
	}
	for y := 0; y < height; y++ {
		set(0, y)
		set(width-1, y)
	}

	// Ruler ticks on the left inside edge: y in dots == mm * 8.
	for y := 8; y < height-8; y += 8 {
		n := 6
		if y%(8*5) == 0 {
			n = 14
		}
		if y%(8*10) == 0 {
			n = 22
		}
		for x := 1; x < 1+n && x < width-1; x++ {
			set(x, y)
		}
	}

	// Diagonals across the middle band.
	midTop := height / 4
	midBot := height / 2
	for i := 0; i < width && midTop+i < midBot; i++ {
		set(i, midTop+i)
		set(width-1-i, midTop+i)
	}

	// Gradient band (exercises dithering): horizontal ramp.
	gTop := height/2 + 4
	gBot := gTop + height/6
	for y := gTop; y < gBot && y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			v := uint8(255 - (x*255)/(width-2))
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}

	// Solid bars at the bottom: black bar, then white gap, then
	// checkerboard.
	bTop := gBot + 4
	for y := bTop; y < bTop+16 && y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			set(x, y)
		}
	}
	cTop := bTop + 24
	for y := cTop; y < cTop+16 && y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			if (x/4+y/4)%2 == 0 {
				set(x, y)
			}
		}
	}
	return img
}
