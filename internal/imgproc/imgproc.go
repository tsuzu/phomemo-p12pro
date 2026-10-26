// Package imgproc converts ordinary images (BMP/PNG/JPEG) into the
// 1-bit packed bitmap the printer's print head consumes.
//
// Pipeline: decode -> grayscale (BT.601) -> rotate -> fit to print-head
// width -> binarize (Floyd-Steinberg dithering or fixed threshold) ->
// pack bits (MSB first, black = 1).
package imgproc

import (
	"fmt"
	"image"
	"image/color"
	"os"

	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp" // register BMP decoder with image.Decode
	"golang.org/x/image/draw"
)

// Bitmap is a 1-bit image in printer orientation: Width is across the
// print head, Height is along the paper feed direction. Data is packed
// row-major, MSB first, 1 = black (heat on).
type Bitmap struct {
	Width  int
	Height int
	Data   []byte // len == BytesPerRow()*Height
}

// BytesPerRow returns the number of bytes needed for one row.
func (b *Bitmap) BytesPerRow() int { return (b.Width + 7) / 8 }

// Black reports whether pixel (x, y) is black.
func (b *Bitmap) Black(x, y int) bool {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return false
	}
	return b.Data[y*b.BytesPerRow()+x/8]&(0x80>>uint(x%8)) != 0
}

// DitherMode selects the binarization algorithm.
type DitherMode int

const (
	// DitherFloydSteinberg applies Floyd-Steinberg error diffusion.
	// Best for photos / grayscale graphics.
	DitherFloydSteinberg DitherMode = iota
	// DitherThreshold applies a plain fixed threshold.
	// Best for line art, text, and pre-binarized BMPs.
	DitherThreshold
	// DitherNone treats the (already fitted) grayscale image as-is:
	// pixels darker than the threshold are black, same as Threshold
	// but kept as a separate name for CLI clarity. Currently aliases
	// DitherThreshold.
	DitherNone = DitherThreshold
)

// Alignment is the horizontal placement when the image is narrower
// than the print head.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignCenter
	AlignRight
)

// Options controls Process.
type Options struct {
	// PrinterWidth is the print-head width in dots. P12 Pro: 96
	// (12mm at 203dpi = 8 dots/mm). Must be > 0.
	PrinterWidth int
	// Rotate is applied to the input before fitting: 0, 90, -90 or 180
	// (degrees, clockwise). Label images are normally authored with
	// the tape running horizontally, so the default CLI value is 90:
	// the authored width becomes the feed-direction length.
	Rotate int
	// Dither selects binarization.
	Dither DitherMode
	// Threshold in [0,255]; pixels darker than this become black.
	// Default 128.
	Threshold int
	// Invert swaps black and white before binarization.
	Invert bool
	// Align places a narrower-than-head image horizontally.
	Align Alignment
	// NoUpscale, when true, never enlarges an image that is narrower
	// than the head (it is padded instead). When false, a narrower
	// image is scaled up to the head width.
	NoUpscale bool
}

func (o *Options) applyDefaults() {
	if o.PrinterWidth <= 0 {
		o.PrinterWidth = 96
	}
	if o.Threshold <= 0 {
		o.Threshold = 128
	}
}

// LoadFile decodes an image file (BMP, PNG or JPEG).
func LoadFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	_ = format
	return img, nil
}

// toGray converts any image to *image.Gray using BT.601 luminance,
// compositing alpha over white (transparent = blank label).
func toGray(src image.Image) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			// Composite over white.
			a := uint32(c.A)
			r := (uint32(c.R)*a + 255*(255-a)) / 255
			g := (uint32(c.G)*a + 255*(255-a)) / 255
			bl := (uint32(c.B)*a + 255*(255-a)) / 255
			lum := (299*r + 587*g + 114*bl + 500) / 1000
			dst.SetGray(x, y, color.Gray{Y: uint8(lum)})
		}
	}
	return dst
}

// rotateGray rotates a grayscale image by 0/90/-90/180 degrees clockwise.
func rotateGray(src *image.Gray, deg int) (*image.Gray, error) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	switch deg {
	case 0:
		dst := image.NewGray(src.Bounds())
		copy(dst.Pix, src.Pix)
		return dst, nil
	case 180:
		dst := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetGray(w-1-x, h-1-y, src.GrayAt(x, y))
			}
		}
		return dst, nil
	case 90: // clockwise: dst(x', y') = src(y', h-1-x')
		dst := image.NewGray(image.Rect(0, 0, h, w))
		for y := 0; y < w; y++ { // dst height = w
			for x := 0; x < h; x++ { // dst width = h
				dst.SetGray(x, y, src.GrayAt(y, h-1-x))
			}
		}
		return dst, nil
	case -90, 270: // counter-clockwise
		dst := image.NewGray(image.Rect(0, 0, h, w))
		for y := 0; y < w; y++ {
			for x := 0; x < h; x++ {
				dst.SetGray(x, y, src.GrayAt(w-1-y, x))
			}
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("rotate must be 0, 90, -90 or 180, got %d", deg)
	}
}

// fit scales/pads src so its width equals printerWidth, preserving
// aspect ratio when scaling.
func fit(src *image.Gray, printerWidth int, align Alignment, noUpscale bool) *image.Gray {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w == printerWidth {
		return src
	}
	if w > printerWidth || !noUpscale {
		// Scale to printerWidth, proportional height.
		nh := int(float64(h)*float64(printerWidth)/float64(w) + 0.5)
		if nh < 1 {
			nh = 1
		}
		dst := image.NewGray(image.Rect(0, 0, printerWidth, nh))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		return dst
	}
	// Pad with white to printerWidth.
	dst := image.NewGray(image.Rect(0, 0, printerWidth, h))
	for i := range dst.Pix {
		dst.Pix[i] = 0xFF
	}
	var off int
	switch align {
	case AlignLeft:
		off = 0
	case AlignRight:
		off = printerWidth - w
	default: // center
		off = (printerWidth - w) / 2
	}
	draw.Copy(dst, image.Pt(off, 0), src, src.Bounds(), draw.Src, nil)
	return dst
}

// binarize converts a fitted grayscale image to a packed Bitmap.
func binarize(src *image.Gray, opt Options) *Bitmap {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bm := &Bitmap{Width: w, Height: h, Data: make([]byte, ((w+7)/8)*h)}

	// Working float buffer for error diffusion.
	buf := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float64(src.GrayAt(x, y).Y)
			if opt.Invert {
				v = 255 - v
			}
			buf[y*w+x] = v
		}
	}

	setBlack := func(x, y int) {
		bm.Data[y*bm.BytesPerRow()+x/8] |= 0x80 >> uint(x%8)
	}
	thr := float64(opt.Threshold)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			old := buf[y*w+x]
			var nv float64
			if old < thr {
				nv = 0
				setBlack(x, y)
			} else {
				nv = 255
			}
			if opt.Dither != DitherFloydSteinberg {
				continue
			}
			err := old - nv
			if x+1 < w {
				buf[y*w+x+1] += err * 7 / 16
			}
			if y+1 < h {
				if x-1 >= 0 {
					buf[(y+1)*w+x-1] += err * 3 / 16
				}
				buf[(y+1)*w+x] += err * 5 / 16
				if x+1 < w {
					buf[(y+1)*w+x+1] += err * 1 / 16
				}
			}
		}
	}
	return bm
}

// Process runs the full pipeline on src.
func Process(src image.Image, opt Options) (*Bitmap, error) {
	opt.applyDefaults()
	gray := toGray(src)
	rot, err := rotateGray(gray, opt.Rotate)
	if err != nil {
		return nil, err
	}
	fitted := fit(rot, opt.PrinterWidth, opt.Align, opt.NoUpscale)
	return binarize(fitted, opt), nil
}

// ScaleNN upscales a grayscale image by an integer factor using
// nearest-neighbour, keeping pixels crisp.
func ScaleNN(src *image.Gray, factor int) *image.Gray {
	if factor <= 1 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewGray(image.Rect(0, 0, w*factor, h*factor))
	for y := 0; y < h*factor; y++ {
		for x := 0; x < w*factor; x++ {
			dst.SetGray(x, y, src.GrayAt(x/factor, y/factor))
		}
	}
	return dst
}

// ToGrayImage renders a Bitmap back to a grayscale image (black on
// white), useful for previews.
func (b *Bitmap) ToGrayImage() *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, b.Width, b.Height))
	for y := 0; y < b.Height; y++ {
		for x := 0; x < b.Width; x++ {
			if b.Black(x, y) {
				dst.SetGray(x, y, color.Gray{Y: 0})
			} else {
				dst.SetGray(x, y, color.Gray{Y: 0xFF})
			}
		}
	}
	return dst
}
