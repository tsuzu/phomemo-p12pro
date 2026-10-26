//go:build js && wasm

// Command wasm exposes the p12pro rendering pipeline to the browser.
//
// It registers three global functions; each returns an object
// {initPackets, payload, preview, width, height} or {error}:
//
//	p12proRenderImage(imageBytes: Uint8Array, opts) — BMP/PNG/JPEG -> job
//	p12proRenderText(lines: string[], fontBytes: Uint8Array, opts)
//	p12proTestPattern(opts)
//
// The job is a P12 native protocol job (see internal/p12):
// "initPackets" is an array of Uint8Array the page sends first,
// one write each with a response wait after each; "payload" is
// the body (ESC/POS raster header + data + feeds) sent afterwards
// in paced chunks. "preview" is a PNG of the 1-bit bitmap, rotated
// back to authored (reading) orientation when the job rotation is
// +/-90.
//
// The Bluetooth transport intentionally lives in JS (Web Bluetooth);
// everything protocol/image related is shared Go code with the CLI.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"syscall/js"

	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
	"github.com/tsuzu/phomemo-p12pro/internal/p12"
	"github.com/tsuzu/phomemo-p12pro/internal/textlabel"
)

// version is stamped by the build; keep in sync with releases.
const version = "0.3.0-wasm"

func main() {
	js.Global().Set("p12proVersion", version)
	js.Global().Set("p12proRenderImage", js.FuncOf(wrap(renderImage)))
	js.Global().Set("p12proRenderText", js.FuncOf(wrap(renderText)))
	js.Global().Set("p12proTestPattern", js.FuncOf(wrap(testPattern)))
	select {} // keep the wasm instance alive
}

// --- JS value helpers -------------------------------------------------

func wrap(fn func(args []js.Value) (js.Value, error)) func(this js.Value, args []js.Value) any {
	return func(this js.Value, args []js.Value) (out any) {
		defer func() {
			if r := recover(); r != nil {
				out = errorResult(fmt.Errorf("%v", r))
			}
		}()
		res, err := fn(args)
		if err != nil {
			return errorResult(err)
		}
		return res
	}
}

func errorResult(err error) js.Value {
	obj := js.Global().Get("Object").New()
	obj.Set("error", err.Error())
	return obj
}

func bytesToJS(b []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	return arr
}

func bytesFromJS(v js.Value) []byte {
	n := v.Get("length").Int()
	b := make([]byte, n)
	js.CopyBytesToGo(b, v)
	return b
}

func optObj(args []js.Value, i int) js.Value {
	if len(args) > i && args[i].Type() == js.TypeObject {
		return args[i]
	}
	return js.Undefined()
}

func optInt(o js.Value, key string, def int) int {
	if o.Type() == js.TypeObject {
		if v := o.Get(key); v.Type() == js.TypeNumber {
			return v.Int()
		}
	}
	return def
}

func optFloat(o js.Value, key string, def float64) float64 {
	if o.Type() == js.TypeObject {
		if v := o.Get(key); v.Type() == js.TypeNumber {
			return v.Float()
		}
	}
	return def
}

func optBool(o js.Value, key string, def bool) bool {
	if o.Type() == js.TypeObject {
		if v := o.Get(key); v.Type() == js.TypeBoolean {
			return v.Bool()
		}
	}
	return def
}

func optString(o js.Value, key string, def string) string {
	if o.Type() == js.TypeObject {
		if v := o.Get(key); v.Type() == js.TypeString {
			return v.String()
		}
	}
	return def
}

// --- shared option plumbing -------------------------------------------

func parseDither(s string) (imgproc.DitherMode, error) {
	switch s {
	case "fs":
		return imgproc.DitherFloydSteinberg, nil
	case "threshold", "none", "":
		return imgproc.DitherThreshold, nil
	default:
		return 0, fmt.Errorf("unknown dither %q", s)
	}
}

func parseAlign(s string) imgproc.Alignment {
	switch s {
	case "left":
		return imgproc.AlignLeft
	case "right":
		return imgproc.AlignRight
	default:
		return imgproc.AlignCenter
	}
}

// result builds the return object. authoredRotate is the rotation
// that was applied to reach printer orientation (90/-90/0); the
// preview is rotated back so it reads naturally on screen.
func result(bm *imgproc.Bitmap, job p12.Job, authoredRotate int) js.Value {
	gray := bm.ToGrayImage()
	switch authoredRotate {
	case 90:
		gray = rotate(gray, -90)
	case -90:
		gray = rotate(gray, 90)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, imgproc.ScaleNN(gray, 2))

	init := js.Global().Get("Array").New(len(job.Init))
	for i, pkt := range job.Init {
		init.SetIndex(i, bytesToJS(pkt))
	}
	obj := js.Global().Get("Object").New()
	obj.Set("initPackets", init)
	obj.Set("payload", bytesToJS(job.Body))
	obj.Set("preview", bytesToJS(buf.Bytes()))
	obj.Set("width", bm.Width)
	obj.Set("height", bm.Height)
	return obj
}

// rotate turns a grayscale image by 90 (clockwise) or -90.
func rotate(src *image.Gray, deg int) *image.Gray {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewGray(image.Rect(0, 0, h, w))
	for y := 0; y < w; y++ {
		for x := 0; x < h; x++ {
			var c color.Gray
			if deg == 90 { // clockwise
				c = src.GrayAt(y, h-1-x)
			} else { // counter-clockwise
				c = src.GrayAt(w-1-y, x)
			}
			dst.SetGray(x, y, c)
		}
	}
	return dst
}

func buildJob(bm *imgproc.Bitmap, o js.Value) p12.Job {
	return p12.BuildJob(bm, optFloat(o, "extraFeedMM", 0))
}

// --- exported functions -----------------------------------------------

func renderImage(args []js.Value) (js.Value, error) {
	if len(args) < 1 {
		return js.Value{}, fmt.Errorf("image bytes required")
	}
	o := optObj(args, 1)
	data := bytesFromJS(args[0])
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return js.Value{}, fmt.Errorf("decode image: %w", err)
	}
	dm, err := parseDither(optString(o, "dither", "fs"))
	if err != nil {
		return js.Value{}, err
	}
	rotateDeg := optInt(o, "rotate", 90)
	bm, err := imgproc.Process(img, imgproc.Options{
		PrinterWidth: optInt(o, "width", 96),
		Rotate:       rotateDeg,
		Dither:       dm,
		Threshold:    optInt(o, "threshold", 128),
		Invert:       optBool(o, "invert", false),
		Align:        parseAlign(optString(o, "align", "center")),
		NoUpscale:    !optBool(o, "scaleUp", false),
	})
	if err != nil {
		return js.Value{}, err
	}
	job := buildJob(bm, o)
	return result(bm, job, rotateDeg), nil
}

func renderText(args []js.Value) (js.Value, error) {
	if len(args) < 2 {
		return js.Value{}, fmt.Errorf("lines and font bytes required")
	}
	o := optObj(args, 2)

	linesVal := args[0]
	var lines []string
	for i := 0; i < linesVal.Length(); i++ {
		if s := linesVal.Index(i).String(); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return js.Value{}, fmt.Errorf("no text lines")
	}
	src, err := textlabel.ParseFont(bytesFromJS(args[1]))
	if err != nil {
		return js.Value{}, err
	}
	width := optInt(o, "width", 96)
	label, err := textlabel.Render(lines, src, textlabel.Options{
		TapeWidthDots:   width,
		MarginY:         optInt(o, "marginY", 4),
		Gap:             optInt(o, "gap", 4),
		PadX:            optInt(o, "padX", 8),
		FontScale:       optFloat(o, "fontScale", 0.85),
		FixedLengthDots: int(optFloat(o, "lengthMM", 0) * 8),
		Align:           optString(o, "align", "center"),
		Supersample:     optInt(o, "supersample", 4),
	})
	if err != nil {
		return js.Value{}, err
	}
	dm, err := parseDither(optString(o, "dither", "threshold"))
	if err != nil {
		return js.Value{}, err
	}
	rotateDeg := optInt(o, "rotate", 90)
	bm, err := imgproc.Process(label, imgproc.Options{
		PrinterWidth: width,
		Rotate:       rotateDeg,
		Dither:       dm,
		Threshold:    optInt(o, "threshold", 128),
		Invert:       optBool(o, "invert", false),
		NoUpscale:    true,
	})
	if err != nil {
		return js.Value{}, err
	}
	job := buildJob(bm, o)
	return result(bm, job, rotateDeg), nil
}

func testPattern(args []js.Value) (js.Value, error) {
	o := optObj(args, 0)
	width := optInt(o, "width", 96)
	height := optInt(o, "height", 320)
	pattern := imgproc.TestPattern(width, height)
	bm, err := imgproc.Process(pattern, imgproc.Options{
		PrinterWidth: width,
		Rotate:       0,
		Dither:       imgproc.DitherFloydSteinberg,
		Threshold:    128,
		NoUpscale:    true,
	})
	if err != nil {
		return js.Value{}, err
	}
	job := buildJob(bm, o)
	// The pattern is drawn in printer orientation; show as-is.
	return result(bm, job, 0), nil
}
