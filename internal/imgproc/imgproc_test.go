package imgproc

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestPackBlackIsOneMSBFirst(t *testing.T) {
	// 8x1, only leftmost pixel black -> 0x80.
	img := solid(8, 1, color.White)
	img.Set(0, 0, color.Black)
	bm, err := Process(img, Options{PrinterWidth: 8, Dither: DitherThreshold, NoUpscale: true})
	if err != nil {
		t.Fatal(err)
	}
	if bm.Width != 8 || bm.Height != 1 || len(bm.Data) != 1 {
		t.Fatalf("bitmap %dx%d len %d", bm.Width, bm.Height, len(bm.Data))
	}
	if bm.Data[0] != 0x80 {
		t.Fatalf("packed byte = %#02x, want 0x80", bm.Data[0])
	}
}

func TestRotate90SwapsDimensions(t *testing.T) {
	// Authored label 200 (tape direction) x 96 (tape width).
	// After a 90-degree rotation it must be 96 wide x 200 high.
	img := solid(200, 96, color.White)
	bm, err := Process(img, Options{PrinterWidth: 96, Rotate: 90, Dither: DitherThreshold})
	if err != nil {
		t.Fatal(err)
	}
	if bm.Width != 96 || bm.Height != 200 {
		t.Fatalf("bitmap %dx%d, want 96x200", bm.Width, bm.Height)
	}
}

func TestAllWhiteIsAllZero(t *testing.T) {
	img := solid(96, 10, color.White)
	bm, err := Process(img, Options{PrinterWidth: 96, Dither: DitherFloydSteinberg})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range bm.Data {
		if v != 0 {
			t.Fatalf("byte %d = %#02x, want 0", i, v)
		}
	}
}

func TestAllBlackIsAllOnes(t *testing.T) {
	img := solid(96, 10, color.Black)
	bm, err := Process(img, Options{PrinterWidth: 96, Dither: DitherFloydSteinberg})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range bm.Data {
		if v != 0xFF {
			t.Fatalf("byte %d = %#02x, want 0xff", i, v)
		}
	}
}

func TestScaleDownToHeadWidth(t *testing.T) {
	img := solid(192, 20, color.White)
	bm, err := Process(img, Options{PrinterWidth: 96, Dither: DitherThreshold})
	if err != nil {
		t.Fatal(err)
	}
	if bm.Width != 96 || bm.Height != 10 {
		t.Fatalf("bitmap %dx%d, want 96x10", bm.Width, bm.Height)
	}
}
