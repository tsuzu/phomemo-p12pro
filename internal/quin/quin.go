// Package quin implements the printer protocol spoken by Zhuhai Quin
// Technology thermal printers — the family behind the "Print Master"
// app: Phomemo D30/D35/Q30, Marklife P11/P12/P50, and (to be verified
// on real hardware) the Phomemo P12 Pro.
//
// The protocol is BLE GATT based:
//
//	service        FF00  0000ff00-0000-1000-8000-00805f9b34fb
//	write char.    FF02  commands + image data are written here
//	notify char.   FF03  flow control / status notifications
//
// Commands to FF02 are mostly prefixed with 0x1F. Image data is sent
// as a single 0x1F 0x10 command carrying a ZLIB stream (RFC 1950,
// stored/deflate level-0, 1KB-window header 0x28 0x15) of the packed
// 1-bit bitmap. Flow control on FF03 is credit based: notification
// [0x01, n] grants n more 200-byte chunks.
//
// This byte layout follows the reverse-engineered implementation in
// the Dart library "print_master_ble" (Protocol 0x1F), cross-checked
// against other Print Master implementations. It has NOT yet been
// verified against a physical P12 Pro — see README "Verification".
package quin

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/adler32"

	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
)

// BLE identifiers.
const (
	ServiceUUID        = "0000ff00-0000-1000-8000-00805f9b34fb"
	WriteCharUUID      = "0000ff02-0000-1000-8000-00805f9b34fb"
	FlowControlCharUUID = "0000ff03-0000-1000-8000-00805f9b34fb"

	ServiceUUID16        = 0xFF00
	WriteCharUUID16      = 0xFF02
	FlowControlCharUUID16 = 0xFF03
)

// PaperType for SetPaperType / printer location.
type PaperType byte

const (
	PaperContinuous PaperType = 0x10 // continuous tape (P12 Pro cassettes)
	PaperGap        PaperType = 0x20 // die-cut labels with gaps
	PaperBlackMark  PaperType = 0x30 // labels with black marks
)

// StartPrintJob begins a print session: 1F C0 01 00.
func StartPrintJob() []byte { return []byte{0x1F, 0xC0, 0x01, 0x00} }

// StopPrintJob ends a print session: 1F C0 01 01.
func StopPrintJob() []byte { return []byte{0x1F, 0xC0, 0x01, 0x01} }

// SetPaperType: 1F 80 01 <type>.
func SetPaperType(t PaperType) []byte { return []byte{0x1F, 0x80, 0x01, byte(t)} }

// SetDensity: 1F 70 01 <level>, level 1 (light) .. 15 (dark).
func SetDensity(level int) []byte {
	if level < 1 {
		level = 1
	}
	if level > 15 {
		level = 15
	}
	return []byte{0x1F, 0x70, 0x01, byte(level)}
}

// SetSpeed: 1F 60 01 <level>, 0=low, 1=medium, 2=high.
func SetSpeed(level int) []byte { return []byte{0x1F, 0x60, 0x01, byte(level)} }

// AlignPaperStart retracts/aligns paper to the start position: 1F 11 51.
func AlignPaperStart() []byte { return []byte{0x1F, 0x11, 0x51} }

// FeedPaperEnd feeds paper to the end/cut position: 1F 11 50.
func FeedPaperEnd() []byte { return []byte{0x1F, 0x11, 0x50} }

// FeedDots feeds n dots forward: 1F 11 00 <hi> <lo>.
func FeedDots(n int) []byte {
	return []byte{0x1F, 0x11, 0x00, byte(n >> 8), byte(n)}
}

// Status / identity queries (responses arrive on FF03).
func QueryStatus() []byte        { return []byte{0x1F, 0x20, 0x00} }
func QueryModel() []byte         { return []byte{0x10, 0xFF, 0x20, 0xF0} }
func QueryFirmware() []byte      { return []byte{0x10, 0xFF, 0x20, 0xF1} }
func QuerySerial() []byte        { return []byte{0x10, 0xFF, 0x20, 0xF2} }
func QueryBatteryVoltage() []byte { return []byte{0x10, 0xFF, 0x50, 0xF1} }
func SelfCheck() []byte          { return []byte{0x1F, 0x40} }

// zlibStored builds an RFC 1950 ZLIB stream whose deflate payload is
// stored (uncompressed), matching what the printer firmware expects:
// header 0x28 0x15 advertises a 1KB deflate window ((0x28<<8|0x15)
// is divisible by 31, as required for FCHECK).
func zlibStored(data []byte) ([]byte, error) {
	var deflateBuf bytes.Buffer
	w, err := flate.NewWriter(&deflateBuf, flate.NoCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	out := make([]byte, 0, deflateBuf.Len()+6)
	out = append(out, 0x28, 0x15)
	out = append(out, deflateBuf.Bytes()...)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], adler32.Checksum(data))
	out = append(out, sum[:]...)
	return out, nil
}

// ImageCommand wraps a packed bitmap into the 0x1F 0x10 image command:
//
//	1F 10 <bytesPerRow BE u16> <height BE u16> <zlibLen BE u32> <zlib stream>
func ImageCommand(bm *imgproc.Bitmap) ([]byte, error) {
	if bm.Width <= 0 || bm.Height <= 0 {
		return nil, fmt.Errorf("empty bitmap %dx%d", bm.Width, bm.Height)
	}
	if bm.Height > 0xFFFF {
		return nil, fmt.Errorf("bitmap too tall: %d dots (max 65535)", bm.Height)
	}
	if len(bm.Data) != bm.BytesPerRow()*bm.Height {
		return nil, fmt.Errorf("bitmap data size %d, want %d", len(bm.Data), bm.BytesPerRow()*bm.Height)
	}
	comp, err := zlibStored(bm.Data)
	if err != nil {
		return nil, err
	}
	cmd := make([]byte, 10, 10+len(comp))
	cmd[0] = 0x1F
	cmd[1] = 0x10
	binary.BigEndian.PutUint16(cmd[2:4], uint16(bm.BytesPerRow()))
	binary.BigEndian.PutUint16(cmd[4:6], uint16(bm.Height))
	binary.BigEndian.PutUint32(cmd[6:10], uint32(len(comp)))
	cmd = append(cmd, comp...)
	return cmd, nil
}

// JobOptions controls BuildJob.
type JobOptions struct {
	Paper PaperType // default PaperContinuous for P12 Pro tape cassettes
	// Density 1..15; 0 leaves the printer setting untouched.
	Density int
	// Speed 0..2; -1 leaves it untouched. Use 0 value carefully:
	// zero means "low" if IncludeSpeed is set. Simpler: if SpeedSet.
	Speed    int
	SpeedSet bool
	// FeedAfterDots, when > 0, feeds extra dots after the job
	// (in addition to FeedPaperEnd). Tape printers often need a
	// few mm of lead-out so the print reaches the cutter.
	FeedAfterDots int
}

// BuildJob assembles the full byte stream for one print job:
//
//	SetPaperType, [SetDensity], [SetSpeed], StartPrintJob,
//	AlignPaperStart, ImageCommand, StopPrintJob, FeedPaperEnd,
//	[FeedDots]
func BuildJob(bm *imgproc.Bitmap, opt JobOptions) ([]byte, error) {
	if opt.Paper == 0 {
		opt.Paper = PaperContinuous
	}
	img, err := ImageCommand(bm)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(SetPaperType(opt.Paper))
	if opt.Density > 0 {
		buf.Write(SetDensity(opt.Density))
	}
	if opt.SpeedSet {
		buf.Write(SetSpeed(opt.Speed))
	}
	buf.Write(StartPrintJob())
	buf.Write(AlignPaperStart())
	buf.Write(img)
	buf.Write(StopPrintJob())
	buf.Write(FeedPaperEnd())
	if opt.FeedAfterDots > 0 {
		buf.Write(FeedDots(opt.FeedAfterDots))
	}
	return buf.Bytes(), nil
}
