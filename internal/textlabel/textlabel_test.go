package textlabel

import (
	"image"
	"os"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
)

// fixedFactory always returns the same face regardless of size,
// so layout can be tested without a font file.
type fixedFactory struct{ face font.Face }

func (f fixedFactory) Face(float64) (font.Face, error) { return f.face, nil }

func blackInRegion(img *image.Gray, y0, y1 int) bool {
	b := img.Bounds()
	for y := y0; y < y1 && y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if img.GrayAt(x, y).Y < 128 {
				return true
			}
		}
	}
	return false
}

func TestRenderTwoLines(t *testing.T) {
	ff := fixedFactory{face: basicfont.Face7x13}
	img, err := Render([]string{"AB", "CD"}, ff, Options{Supersample: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dy(); got != 96 {
		t.Fatalf("height = %d, want 96", got)
	}
	if got := img.Bounds().Dx(); got <= 0 {
		t.Fatalf("width = %d", got)
	}
	// Both rows must contain glyphs: upper slot and lower slot.
	if !blackInRegion(img, 0, 48) {
		t.Error("no black pixels in upper half")
	}
	if !blackInRegion(img, 48, 96) {
		t.Error("no black pixels in lower half")
	}
}

func TestRenderFixedLength(t *testing.T) {
	ff := fixedFactory{face: basicfont.Face7x13}
	img, err := Render([]string{"A", "B"}, ff, Options{Supersample: 1, FixedLengthDots: 320})
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dx(); got != 320 {
		t.Fatalf("width = %d, want 320", got)
	}
}

func TestRenderSingleLine(t *testing.T) {
	ff := fixedFactory{face: basicfont.Face7x13}
	img, err := Render([]string{"HELLO"}, ff, Options{Supersample: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !blackInRegion(img, 0, 96) {
		t.Error("single line produced no glyphs")
	}
}

// TestRenderJapanese exercises the real sfnt path when a CJK font is
// available on the machine (skipped otherwise).
func TestRenderJapanese(t *testing.T) {
	path, err := DefaultFontPath()
	if err != nil {
		t.Skip("no system font:", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("font not present:", path)
	}
	src, err := LoadFont(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := Render([]string{"調味料", "しょうゆ"}, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dy() != 96 {
		t.Fatalf("height = %d, want 96", img.Bounds().Dy())
	}
	if !blackInRegion(img, 0, 48) || !blackInRegion(img, 48, 96) {
		t.Error("Japanese lines did not render in both rows")
	}
}
