package quin

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"testing"

	"github.com/tsuzu/phomemo-p12pro/internal/imgproc"
)

func testBitmap() *imgproc.Bitmap {
	// 16 wide x 2 high, first row all black, second all white.
	data := []byte{0xFF, 0xFF, 0x00, 0x00}
	return &imgproc.Bitmap{Width: 16, Height: 2, Data: data}
}

func TestZlibStoredRoundTrip(t *testing.T) {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11}
	stream, err := zlibStored(data)
	if err != nil {
		t.Fatal(err)
	}
	if stream[0] != 0x28 || stream[1] != 0x15 {
		t.Fatalf("zlib header = %02x %02x, want 28 15", stream[0], stream[1])
	}
	r, err := zlib.NewReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip = %x, want %x", got, data)
	}
}

func TestImageCommandHeader(t *testing.T) {
	bm := testBitmap()
	cmd, err := ImageCommand(bm)
	if err != nil {
		t.Fatal(err)
	}
	if cmd[0] != 0x1F || cmd[1] != 0x10 {
		t.Fatalf("prefix = %02x %02x, want 1f 10", cmd[0], cmd[1])
	}
	if v := binary.BigEndian.Uint16(cmd[2:4]); v != 2 {
		t.Fatalf("bytesPerRow field = %d, want 2", v)
	}
	if v := binary.BigEndian.Uint16(cmd[4:6]); v != 2 {
		t.Fatalf("height field = %d, want 2", v)
	}
	if v := binary.BigEndian.Uint32(cmd[6:10]); int(v) != len(cmd)-10 {
		t.Fatalf("compressed length field = %d, actual %d", v, len(cmd)-10)
	}
	// Payload must zlib-decompress to the raw bitmap.
	r, err := zlib.NewReader(bytes.NewReader(cmd[10:]))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bm.Data) {
		t.Fatalf("image payload = %x, want %x", got, bm.Data)
	}
}

func TestBuildJobSequence(t *testing.T) {
	bm := testBitmap()
	job, err := BuildJob(bm, JobOptions{Paper: PaperContinuous, Density: 8})
	if err != nil {
		t.Fatal(err)
	}
	// SetPaperType(4) + SetDensity(4) + Start(4) + AlignStart(3)
	// then image command, then Stop(4) + FeedEnd(3).
	prefix := []byte{
		0x1F, 0x80, 0x01, 0x10, // paper continuous
		0x1F, 0x70, 0x01, 0x08, // density 8
		0x1F, 0xC0, 0x01, 0x00, // start
		0x1F, 0x11, 0x51, // align start
		0x1F, 0x10, // image command begins
	}
	if !bytes.HasPrefix(job, prefix) {
		t.Fatalf("job prefix = %x, want %x", job[:len(prefix)], prefix)
	}
	suffix := []byte{
		0x1F, 0xC0, 0x01, 0x01, // stop
		0x1F, 0x11, 0x50, // feed end
	}
	if !bytes.HasSuffix(job, suffix) {
		t.Fatalf("job suffix = %x, want %x", job[len(job)-len(suffix):], suffix)
	}
}

func TestDefaultPaperIsContinuous(t *testing.T) {
	job, err := BuildJob(testBitmap(), JobOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(job, []byte{0x1F, 0x80, 0x01, 0x10}) {
		t.Fatalf("default paper prefix = %x", job[:4])
	}
}
