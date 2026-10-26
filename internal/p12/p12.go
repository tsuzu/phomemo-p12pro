// Package p12 implements the Phomemo P12 / P12 Pro native print
// protocol: a proprietary 0x1F 0x11 init sequence, then an ESC/POS
// raster image (GS v 0), then ESC d paper feeds.
//
// This is the protocol the P12 family actually speaks (reverse
// engineered by soburi/phomemo_p12 and used by the phomymo web
// app, which prints to the P12 Pro successfully). It is NOT the
// Quin 0x1F job protocol of the D30 family (internal/quin): on
// real hardware (2026-10-26) a P12 Pro accepted BLE writes
// happily — the module ACKs and grants flow-control credits —
// but silently ignored Quin jobs, self-check (1F 40) and feed
// (1F 11 50) commands, with zero paper motion.
package p12

import (
	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
)

// InitPackets are sent before the job body, one write each, with a
// short wait for a printer response (any FF03 notification) after
// each packet. The grouping into packets matters.
var InitPackets = [][]byte{
	{0x1F, 0x11, 0x38},
	{0x1F, 0x11, 0x11, 0x1F, 0x11, 0x12, 0x1F, 0x11, 0x09, 0x1F, 0x11, 0x13},
	{0x1F, 0x11, 0x09},
	{0x1F, 0x11, 0x19, 0x1F, 0x11, 0x11},
	{0x1F, 0x11, 0x19},
	{0x1F, 0x11, 0x07},
}

// Header starts the raster image: ESC @ (initialize), then
// GS v 0 mode 0 with little-endian width in bytes and height in
// rows. The bitmap that follows is row-major, MSB first, 1=black —
// exactly imgproc.Bitmap's layout in printer orientation.
func Header(b *imgproc.Bitmap) []byte {
	wb := b.BytesPerRow()
	return []byte{
		0x1B, 0x40,
		0x1D, 0x76, 0x30, 0x00,
		byte(wb), byte(wb >> 8),
		byte(b.Height), byte(b.Height >> 8),
	}
}

// Feed returns ESC d n: feed n lines (rows).
func Feed(lines int) []byte {
	if lines < 0 {
		lines = 0
	}
	if lines > 255 {
		lines = 255
	}
	return []byte{0x1B, 0x64, byte(lines)}
}

// Job is one P12 print job: the init packets, then the body
// (header + raster data + feeds). Transports pace them as:
// init packets one by one with a response wait after each, body
// in 128-byte chunks with ~20ms delays, per the reference
// implementations.
type Job struct {
	Init [][]byte
	Body []byte
}

// BuildJob builds a job for bitmap b. After the image, the tape is
// fed by two ESC d 13 commands plus extraFeedMM of extra feed
// (tape lead-out to the cutter), expressed as ESC d commands of at
// most 255 lines each (203dpi = 8 dots/mm).
func BuildJob(b *imgproc.Bitmap, extraFeedMM float64) Job {
	body := make([]byte, 0, len(b.Data)+16)
	body = append(body, Header(b)...)
	body = append(body, b.Data...)
	body = append(body, Feed(13)...)
	body = append(body, Feed(13)...)
	lines := int(extraFeedMM * 8)
	for lines > 0 {
		n := lines
		if n > 255 {
			n = 255
		}
		body = append(body, Feed(n)...)
		lines -= n
	}
	return Job{Init: InitPackets, Body: body}
}

// Stream returns the whole job as one byte stream (init packets
// followed by the body), for file output and serial transports.
func (j Job) Stream() []byte {
	var out []byte
	for _, p := range j.Init {
		out = append(out, p...)
	}
	return append(out, j.Body...)
}
