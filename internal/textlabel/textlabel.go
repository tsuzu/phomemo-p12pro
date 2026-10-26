// Package textlabel renders text lines into a label image in
// "authored" orientation: the image height is the tape width
// (e.g. 96 dots for 12mm tape at 203dpi) and the width is the tape
// feed direction. Lines are stacked vertically — two lines give the
// classic 2-row label. The result feeds imgproc.Process like any
// other image (it is rotated 90 degrees there for printing).
//
// Text is rendered supersampled (default 4x) with an anti-aliased
// sfnt font and downscaled, which keeps CJK strokes legible at the
// ~40-dot line heights a 12mm tape allows for two rows.
package textlabel

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// FaceFactory creates a font.Face for a given pixel size (em size).
type FaceFactory interface {
	Face(sizePx float64) (font.Face, error)
}

// FontSource is a FaceFactory backed by a TTF/OTF/TTC font file.
type FontSource struct {
	fnt *sfnt.Font
}

// LoadFont reads a .ttf/.otf font, or the first font of a .ttc
// collection.
func LoadFont(path string) (*FontSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src, err := ParseFont(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return src, nil
}

// ParseFont parses font data (TTF/OTF, or a TTC collection whose
// first font is used) from memory. This is the entry point used in
// environments without a filesystem, e.g. WebAssembly in a browser
// where the font arrives as uploaded/downloaded bytes.
func ParseFont(data []byte) (*FontSource, error) {
	if len(data) >= 4 && string(data[:4]) == "ttcf" {
		coll, err := sfnt.ParseCollection(data)
		if err != nil {
			return nil, fmt.Errorf("parse font collection: %w", err)
		}
		fnt, err := coll.Font(0)
		if err != nil {
			return nil, fmt.Errorf("font collection: %w", err)
		}
		return &FontSource{fnt: fnt}, nil
	}
	fnt, err := sfnt.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}
	return &FontSource{fnt: fnt}, nil
}

// Face returns an anti-aliased face at sizePx pixels em size.
// Hinting is off because rendering is supersampled and downscaled.
func (s *FontSource) Face(sizePx float64) (font.Face, error) {
	return opentype.NewFace(s.fnt, &opentype.FaceOptions{
		Size:    sizePx,
		DPI:     72, // 1pt = 1px, so Size is the em size in pixels
		Hinting: font.HintingNone,
	})
}

// DefaultFontPath searches well-known locations for a font that
// covers Japanese. Override with the --font flag or P12PRO_FONT.
func DefaultFontPath() (string, error) {
	if p := os.Getenv("P12PRO_FONT"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/System/Library/Fonts/ヒラギノ角ゴシック W4.ttc",
			"/System/Library/Fonts/ヒラギノ角ゴシック W3.ttc",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
			"/Library/Fonts/Arial Unicode.ttf",
		}
	case "windows":
		candidates = []string{
			`C:\Windows\Fonts\YuGothM.ttc`,
			`C:\Windows\Fonts\YuGothR.ttc`,
			`C:\Windows\Fonts\meiryo.ttc`,
			`C:\Windows\Fonts\msgothic.ttc`,
		}
	default: // linux and others
		candidates = []string{
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/opentype/noto/NotoSerifCJK-Regular.ttc",
			"/usr/share/fonts/opentype/ipafont-gothic/ipag.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no usable font found; specify one with --font (searched: %s)", strings.Join(candidates, ", "))
}

// Options controls Render.
type Options struct {
	// TapeWidthDots is the authored image height (= print head
	// width). Default 96 (12mm @ 203dpi).
	TapeWidthDots int
	// MarginY is the blank dots above the first and below the last
	// line. Default 4.
	MarginY int
	// Gap is the blank dots between lines. Default 4.
	Gap int
	// PadX is the blank dots at both ends of the tape direction.
	// Default 8.
	PadX int
	// FontScale is the em size as a fraction of the line slot
	// height. Default 0.85 (font ascent+descent usually exceeds the
	// em size, so 1.0 would bleed into the gap).
	FontScale float64
	// FixedLengthDots, when > 0, forces the authored image width
	// (tape direction) to exactly this many dots; text is placed
	// per Align. Default 0 = fit to the longest line.
	FixedLengthDots int
	// Align places each line within the content width:
	// "left", "center" (default) or "right".
	Align string
	// Supersample renders at this multiple of the final size and
	// downscales. Default 4.
	Supersample int
}

func (o *Options) applyDefaults() {
	if o.TapeWidthDots <= 0 {
		o.TapeWidthDots = 96
	}
	if o.MarginY == 0 {
		o.MarginY = 4
	}
	if o.Gap == 0 {
		o.Gap = 4
	}
	if o.PadX == 0 {
		o.PadX = 8
	}
	if o.FontScale <= 0 {
		o.FontScale = 0.85
	}
	if o.Align == "" {
		o.Align = "center"
	}
	if o.Supersample <= 0 {
		o.Supersample = 4
	}
}

// Render draws lines stacked vertically and returns the authored
// label image (white background, black text, grayscale AA).
func Render(lines []string, ff FaceFactory, opt Options) (*image.Gray, error) {
	opt.applyDefaults()
	if len(lines) == 0 {
		return nil, fmt.Errorf("no lines to render")
	}
	n := len(lines)
	slot := (opt.TapeWidthDots - 2*opt.MarginY - opt.Gap*(n-1)) / n
	if slot < 8 {
		return nil, fmt.Errorf("line slot %d dots too small (tape width %d, %d lines)", slot, opt.TapeWidthDots, n)
	}
	ss := opt.Supersample
	face, err := ff.Face(float64(slot) * opt.FontScale * float64(ss))
	if err != nil {
		return nil, err
	}
	defer face.Close()

	d := &font.Drawer{Face: face}
	adv := make([]int, n) // advances in supersampled pixels
	maxAdv := 0
	for i, s := range lines {
		adv[i] = d.MeasureString(s).Ceil()
		if adv[i] > maxAdv {
			maxAdv = adv[i]
		}
	}

	outW := opt.FixedLengthDots
	if outW <= 0 {
		outW = (maxAdv+ss-1)/ss + 2*opt.PadX
	}
	canvasW := outW * ss
	canvasH := opt.TapeWidthDots * ss
	canvas := image.NewGray(image.Rect(0, 0, canvasW, canvasH))
	for i := range canvas.Pix {
		canvas.Pix[i] = 0xFF
	}

	metrics := face.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	contentW := canvasW - 2*opt.PadX*ss

	d.Dst = canvas
	d.Src = image.NewUniform(color.Black)
	for i, s := range lines {
		slotTop := (opt.MarginY + i*(slot+opt.Gap)) * ss
		slotH := slot * ss
		// Center the ascent..descent box within the slot.
		baseline := slotTop + (slotH-(ascent+descent))/2 + ascent

		var x int
		switch opt.Align {
		case "left":
			x = opt.PadX * ss
		case "right":
			x = opt.PadX*ss + (contentW - adv[i])
		default: // center
			x = opt.PadX*ss + (contentW-adv[i])/2
		}
		if x < 0 {
			x = 0
		}
		d.Dot = fixed.P(x, baseline)
		d.DrawString(s)
	}

	if ss == 1 {
		return canvas, nil
	}
	out := image.NewGray(image.Rect(0, 0, outW, opt.TapeWidthDots))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), canvas, canvas.Bounds(), draw.Src, nil)
	return out, nil
}
