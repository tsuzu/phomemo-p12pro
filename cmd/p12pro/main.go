// Command p12pro prints images to a Phomemo P12 Pro (and other
// Zhuhai Quin / "Print Master" protocol printers) from BMP/PNG/JPEG
// files, over BLE or a serial/RFCOMM device file.
//
// Subcommands:
//
//	render      image -> printer payload (.bin), no hardware needed
//	preview     image -> 1-bit preview PNG, no hardware needed
//	print       image -> printer (BLE by default, or --device file)
//	scan        list nearby BLE devices (marks FF00 printers)
//	probe       dump the GATT tree of a printer (protocol check)
//	testpattern print/render a calibration pattern
package main

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/tsuzu/phomemo-p12pro/internal/bleprinter"
	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
	"github.com/tsuzu/phomemo-p12pro/internal/quin"
	"github.com/tsuzu/phomemo-p12pro/internal/textlabel"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "preview":
		err = cmdPreview(os.Args[2:])
	case "text":
		err = cmdText(os.Args[2:])
	case "print":
		err = cmdPrint(os.Args[2:])
	case "scan":
		err = cmdScan(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "testpattern":
		err = cmdTestPattern(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `p12pro — Phomemo P12 Pro image printer (Go)

Usage:
  p12pro render      -i image.bmp -o payload.bin [image flags] [job flags]
  p12pro preview     -i image.bmp -o preview.png [image flags]
  p12pro text        "上段" "下段" (--name P12 | --address AA:BB:.. | --device /dev/rfcomm0 | -o payload.bin) [--font F] [--length-mm N]
  p12pro print       -i image.bmp (--name P12 | --address AA:BB:.. | --device /dev/rfcomm0) [flags]
  p12pro scan        [--timeout 10s]
  p12pro probe       (--name P12 | --address AA:BB:..)
  p12pro testpattern [-o payload.bin | --name P12 | --device ...] [--height 320]

Printer defaults (P12 Pro): 203dpi, head width 96 dots (12mm tape).
Input images are authored label-style: horizontal = tape direction,
vertical = across the tape; they are rotated 90° by default.
`)
}

// --- shared flag sets -------------------------------------------------

type imageFlags struct {
	input     string
	width     int
	rotate    int
	dither    string
	threshold int
	invert    bool
	align     string
	scaleUp   bool
}

func addImageFlags(fs *flag.FlagSet, f *imageFlags) {
	fs.StringVar(&f.input, "i", "", "input image file (BMP, PNG or JPEG)")
	fs.IntVar(&f.width, "width", 96, "print head width in dots (96 = 12mm @ 203dpi)")
	fs.IntVar(&f.rotate, "rotate", 90, "rotation applied to the input: 0, 90, -90 or 180 (deg, clockwise)")
	fs.StringVar(&f.dither, "dither", "fs", "binarization: fs (Floyd-Steinberg) or threshold")
	fs.IntVar(&f.threshold, "threshold", 128, "binarization threshold 0-255")
	fs.BoolVar(&f.invert, "invert", false, "invert black/white")
	fs.StringVar(&f.align, "align", "center", "placement when narrower than the head: left, center, right")
	fs.BoolVar(&f.scaleUp, "scale-up", false, "scale a narrower image up to the head width (default: pad)")
}

func (f *imageFlags) options() (imgproc.Options, error) {
	var d imgproc.DitherMode
	switch f.dither {
	case "fs":
		d = imgproc.DitherFloydSteinberg
	case "threshold", "none":
		d = imgproc.DitherThreshold
	default:
		return imgproc.Options{}, fmt.Errorf("unknown -dither %q (want fs or threshold)", f.dither)
	}
	var a imgproc.Alignment
	switch f.align {
	case "left":
		a = imgproc.AlignLeft
	case "center":
		a = imgproc.AlignCenter
	case "right":
		a = imgproc.AlignRight
	default:
		return imgproc.Options{}, fmt.Errorf("unknown -align %q", f.align)
	}
	return imgproc.Options{
		PrinterWidth: f.width,
		Rotate:       f.rotate,
		Dither:       d,
		Threshold:    f.threshold,
		Invert:       f.invert,
		Align:        a,
		NoUpscale:    !f.scaleUp,
	}, nil
}

func (f *imageFlags) bitmap() (*imgproc.Bitmap, error) {
	if f.input == "" {
		return nil, fmt.Errorf("-i input image is required")
	}
	opt, err := f.options()
	if err != nil {
		return nil, err
	}
	img, err := imgproc.LoadFile(f.input)
	if err != nil {
		return nil, err
	}
	return imgproc.Process(img, opt)
}

type jobFlags struct {
	paper       string
	density     int
	extraFeedMM float64
}

func addJobFlags(fs *flag.FlagSet, f *jobFlags) {
	fs.StringVar(&f.paper, "paper", "continuous", "paper type: continuous (P12 tape), gap, mark")
	fs.IntVar(&f.density, "density", 0, "print density 1-15 (0 = keep printer setting)")
	fs.Float64Var(&f.extraFeedMM, "extra-feed-mm", 0, "extra feed after printing, in mm (tape lead-out to the cutter)")
}

func (f *jobFlags) options() (quin.JobOptions, error) {
	var p quin.PaperType
	switch f.paper {
	case "continuous":
		p = quin.PaperContinuous
	case "gap":
		p = quin.PaperGap
	case "mark":
		p = quin.PaperBlackMark
	default:
		return quin.JobOptions{}, fmt.Errorf("unknown -paper %q", f.paper)
	}
	return quin.JobOptions{
		Paper:         p,
		Density:       f.density,
		FeedAfterDots: int(f.extraFeedMM * 8), // 203dpi = 8 dots/mm
	}, nil
}

type targetFlags struct {
	address string
	name    string
	device  string
	timeout time.Duration
	chunk   int
	noResp  bool
}

func addTargetFlags(fs *flag.FlagSet, f *targetFlags, withDevice bool) {
	fs.StringVar(&f.address, "address", "", "BLE address of the printer (from `scan`)")
	fs.StringVar(&f.name, "name", "", "BLE name substring to match, e.g. P12")
	fs.DurationVar(&f.timeout, "timeout", 15*time.Second, "BLE scan/connect timeout")
	fs.IntVar(&f.chunk, "chunk", 200, "BLE write chunk size in bytes")
	fs.BoolVar(&f.noResp, "without-response", false, "use BLE write-without-response (faster, less reliable)")
	if withDevice {
		fs.StringVar(&f.device, "device", "", "serial/RFCOMM device file instead of BLE, e.g. /dev/rfcomm0")
	}
}

// sendPayload delivers a built job to the chosen target.
func sendPayload(f *targetFlags, payload []byte) error {
	if f.device != "" {
		return writeDeviceFile(f.device, payload)
	}
	if f.address == "" && f.name == "" {
		return fmt.Errorf("specify --name, --address or --device")
	}
	pr, err := bleprinter.Connect(f.address, f.name, f.timeout)
	if err != nil {
		return err
	}
	defer pr.Close()
	pr.OnNotify = func(data []byte) {
		fmt.Fprintf(os.Stderr, "printer notify: % x\n", data)
	}
	if err := pr.WritePayload(payload, f.chunk, !f.noResp); err != nil {
		return err
	}
	// Keep the link up briefly so the printer can finish the job
	// before we disconnect.
	time.Sleep(2 * time.Second)
	return nil
}

// writeDeviceFile streams payload to a character device
// (/dev/rfcomm0, /dev/ttyACM0, ...) or a regular file.
func writeDeviceFile(path string, payload []byte) error {
	fh, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer fh.Close()
	const chunk = 200
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := fh.Write(payload[off:end]); err != nil {
			return fmt.Errorf("write %s at %d: %w", path, off, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

func parseDither(s string) (imgproc.DitherMode, error) {
	switch s {
	case "fs":
		return imgproc.DitherFloydSteinberg, nil
	case "threshold", "none":
		return imgproc.DitherThreshold, nil
	default:
		return 0, fmt.Errorf("unknown -dither %q (want fs or threshold)", s)
	}
}

// writePreviewPNG writes the 1-bit bitmap as a PNG (black on white),
// nearest-neighbour upscaled by scale.
func writePreviewPNG(path string, bm *imgproc.Bitmap, scale int) error {
	preview := bm.ToGrayImage()
	if scale > 1 {
		preview = imgproc.ScaleNN(preview, scale)
	}
	if path == "" || path == "-" {
		return png.Encode(os.Stdout, preview)
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return png.Encode(fh, preview)
}

func writeOutput(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// --- subcommands ------------------------------------------------------

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	var img imageFlags
	var job jobFlags
	var out string
	addImageFlags(fs, &img)
	addJobFlags(fs, &job)
	fs.StringVar(&out, "o", "", "output payload file (- or empty = stdout)")
	fs.Parse(args)

	bm, err := img.bitmap()
	if err != nil {
		return err
	}
	jo, err := job.options()
	if err != nil {
		return err
	}
	payload, err := quin.BuildJob(bm, jo)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "bitmap %dx%d, payload %d bytes\n", bm.Width, bm.Height, len(payload))
	return writeOutput(out, payload)
}

func cmdPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	var img imageFlags
	var out string
	var scale int
	addImageFlags(fs, &img)
	fs.StringVar(&out, "o", "", "output PNG file (- or empty = stdout)")
	fs.IntVar(&scale, "scale", 4, "nearest-neighbour upscale factor for the preview")
	fs.Parse(args)

	bm, err := img.bitmap()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "bitmap %dx%d\n", bm.Width, bm.Height)
	return writePreviewPNG(out, bm, scale)
}

// cmdText renders 1 or 2 lines of text as a two-row label and
// prints it (or writes the payload / a preview).
//
//	p12pro text "調味料" "しょうゆ" --name P12
//	p12pro text "上段" "下段" -o payload.bin --preview preview.png
func cmdText(args []string) error {
	fs := flag.NewFlagSet("text", flag.ExitOnError)
	var job jobFlags
	var tgt targetFlags
	var out, previewPath, fontPath, align, dither string
	var width, gap, marginY, padX, supersample, threshold, rotate, previewScale int
	var lengthMM, fontScale float64
	var invert bool
	addJobFlags(fs, &job)
	addTargetFlags(fs, &tgt, true)
	fs.StringVar(&out, "o", "", "write payload to file instead of printing")
	fs.StringVar(&previewPath, "preview", "", "also write a 1-bit preview PNG to this path")
	fs.IntVar(&previewScale, "preview-scale", 4, "preview PNG upscale factor")
	fs.StringVar(&fontPath, "font", "", "font file (.ttf/.otf/.ttc); default: auto-detect a Japanese font (or $P12PRO_FONT)")
	fs.Float64Var(&lengthMM, "length-mm", 0, "fixed label length in mm (0 = fit to text)")
	fs.Float64Var(&fontScale, "font-scale", 0.85, "font em size as a fraction of the line height")
	fs.StringVar(&align, "align", "center", "line alignment within the label: left, center, right")
	fs.IntVar(&gap, "gap", 4, "blank dots between the two rows")
	fs.IntVar(&marginY, "margin-y", 4, "blank dots above/below the rows")
	fs.IntVar(&padX, "pad-x", 8, "blank dots at both ends of the label")
	fs.IntVar(&supersample, "supersample", 4, "rendering supersample factor (higher = smoother)")
	fs.IntVar(&width, "width", 96, "tape width in dots (96 = 12mm @ 203dpi)")
	fs.IntVar(&rotate, "rotate", 90, "rotation to printer orientation: 90 or -90")
	fs.StringVar(&dither, "dither", "threshold", "binarization: threshold (crisp text) or fs")
	fs.IntVar(&threshold, "threshold", 128, "binarization threshold 0-255")
	fs.BoolVar(&invert, "invert", false, "invert black/white (white text on black)")

	// stdlib flag stops at the first positional argument; parse in a
	// loop so flags may also come after the text lines.
	var lines []string
	rest := args
	for len(rest) > 0 {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) > 0 {
			lines = append(lines, rest[0])
			rest = rest[1:]
		}
	}
	if len(lines) < 1 || len(lines) > 2 {
		return fmt.Errorf("usage: p12pro text [flags] <line1> [line2]")
	}
	if fontPath == "" {
		p, err := textlabel.DefaultFontPath()
		if err != nil {
			return err
		}
		fontPath = p
	}
	src, err := textlabel.LoadFont(fontPath)
	if err != nil {
		return err
	}
	labelImg, err := textlabel.Render(lines, src, textlabel.Options{
		TapeWidthDots:   width,
		MarginY:         marginY,
		Gap:             gap,
		PadX:            padX,
		FontScale:       fontScale,
		FixedLengthDots: int(lengthMM * 8), // 203dpi = 8 dots/mm
		Align:           align,
		Supersample:     supersample,
	})
	if err != nil {
		return err
	}
	dm, err := parseDither(dither)
	if err != nil {
		return err
	}
	bm, err := imgproc.Process(labelImg, imgproc.Options{
		PrinterWidth: width,
		Rotate:       rotate,
		Dither:       dm,
		Threshold:    threshold,
		Invert:       invert,
		NoUpscale:    true,
	})
	if err != nil {
		return err
	}
	jo, err := job.options()
	if err != nil {
		return err
	}
	payload, err := quin.BuildJob(bm, jo)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "text label %dx%d (authored %dx%d, font %s), payload %d bytes\n",
		bm.Width, bm.Height, labelImg.Bounds().Dx(), labelImg.Bounds().Dy(), fontPath, len(payload))
	if previewPath != "" {
		if err := writePreviewPNG(previewPath, bm, previewScale); err != nil {
			return err
		}
	}
	if out != "" {
		return writeOutput(out, payload)
	}
	if err := sendPayload(&tgt, payload); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "done")
	return nil
}

func cmdPrint(args []string) error {
	fs := flag.NewFlagSet("print", flag.ExitOnError)
	var img imageFlags
	var job jobFlags
	var tgt targetFlags
	addImageFlags(fs, &img)
	addJobFlags(fs, &job)
	addTargetFlags(fs, &tgt, true)
	fs.Parse(args)

	bm, err := img.bitmap()
	if err != nil {
		return err
	}
	jo, err := job.options()
	if err != nil {
		return err
	}
	payload, err := quin.BuildJob(bm, jo)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "printing bitmap %dx%d, payload %d bytes\n", bm.Width, bm.Height, len(payload))
	if err := sendPayload(&tgt, payload); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "done")
	return nil
}

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	timeout := fs.Duration("timeout", 10*time.Second, "scan duration")
	fs.Parse(args)

	devices, err := bleprinter.Scan(*timeout)
	if err != nil {
		return err
	}
	for _, d := range devices {
		mark := ""
		if d.HasPrinterSvc {
			mark = "  <- printer service FF00"
		}
		fmt.Printf("%-20s rssi=%4d name=%q%s\n", d.Address, d.RSSI, d.Name, mark)
	}
	return nil
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	var tgt targetFlags
	addTargetFlags(fs, &tgt, false)
	fs.Parse(args)
	if tgt.address == "" && tgt.name == "" {
		return fmt.Errorf("specify --name or --address")
	}
	services, err := bleprinter.Probe(tgt.address, tgt.name, tgt.timeout)
	if err != nil {
		return err
	}
	for _, s := range services {
		fmt.Printf("service %s\n", s.UUID)
		for _, c := range s.Chars {
			fmt.Printf("  char %s\n", c.UUID)
		}
	}
	fmt.Println()
	fmt.Println("Quin / Print Master protocol check: service 0000ff00-... with")
	fmt.Println("chars 0000ff02-... (write) and 0000ff03-... (flow control)")
	fmt.Println("must be present for this tool's print path to work.")
	return nil
}

func cmdTestPattern(args []string) error {
	fs := flag.NewFlagSet("testpattern", flag.ExitOnError)
	var job jobFlags
	var tgt targetFlags
	var out string
	var width, height int
	addJobFlags(fs, &job)
	addTargetFlags(fs, &tgt, true)
	fs.StringVar(&out, "o", "", "write payload to file instead of printing")
	fs.IntVar(&width, "width", 96, "pattern width in dots")
	fs.IntVar(&height, "height", 320, "pattern height in dots (feed direction)")
	fs.Parse(args)

	pattern := imgproc.TestPattern(width, height)
	bm, err := imgproc.Process(pattern, imgproc.Options{
		PrinterWidth: width,
		Rotate:       0,
		Dither:       imgproc.DitherFloydSteinberg,
		Threshold:    128,
		NoUpscale:    true,
	})
	if err != nil {
		return err
	}
	jo, err := job.options()
	if err != nil {
		return err
	}
	payload, err := quin.BuildJob(bm, jo)
	if err != nil {
		return err
	}
	if out != "" {
		return writeOutput(out, payload)
	}
	return sendPayload(&tgt, payload)
}
