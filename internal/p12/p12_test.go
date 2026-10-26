package p12

import (
	"bytes"
	"testing"

	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
)

func TestBuildJobLayout(t *testing.T) {
	bm := &imgproc.Bitmap{Width: 96, Height: 320, Data: make([]byte, 12*320)}
	job := BuildJob(bm, 0)

	if len(job.Init) != 6 {
		t.Fatalf("init packets = %d, want 6", len(job.Init))
	}
	if !bytes.Equal(job.Init[0], []byte{0x1F, 0x11, 0x38}) {
		t.Errorf("init[0] = % x", job.Init[0])
	}
	// Header: ESC @, GS v 0 m=0, widthBytes=12 LE, height=320 LE.
	wantHead := []byte{0x1B, 0x40, 0x1D, 0x76, 0x30, 0x00, 12, 0, 0x40, 0x01}
	if !bytes.HasPrefix(job.Body, wantHead) {
		t.Errorf("body head = % x, want prefix % x", job.Body[:10], wantHead)
	}
	// Tail: two ESC d 13.
	wantTail := []byte{0x1B, 0x64, 0x0D, 0x1B, 0x64, 0x0D}
	if !bytes.HasSuffix(job.Body, wantTail) {
		t.Errorf("body tail = % x, want suffix % x", job.Body[len(job.Body)-6:], wantTail)
	}
	if len(job.Body) != 10+12*320+6 {
		t.Errorf("body len = %d", len(job.Body))
	}
	// Stream = init (3+12+3+6+3+3=30 bytes) + body.
	if len(job.Stream()) != 30+len(job.Body) {
		t.Errorf("stream len = %d", len(job.Stream()))
	}
}

func TestExtraFeed(t *testing.T) {
	bm := &imgproc.Bitmap{Width: 96, Height: 8, Data: make([]byte, 12*8)}
	job := BuildJob(bm, 40) // 40mm = 320 lines -> 255 + 65
	tail := job.Body[len(job.Body)-12:]
	want := []byte{0x1B, 0x64, 0x0D, 0x1B, 0x64, 0x0D, 0x1B, 0x64, 0xFF, 0x1B, 0x64, 0x41}
	if !bytes.Equal(tail, want) {
		t.Errorf("tail = % x, want % x", tail, want)
	}
}
