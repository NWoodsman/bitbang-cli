package bytestream

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type shortWriter struct {
	bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.Buffer.Write(p)
}

type frameCollector struct {
	dat bytes.Buffer
	fin int
}

func (w *frameCollector) WriteDAT(p []byte) error {
	_, err := w.dat.Write(p)
	return err
}
func (w *frameCollector) WriteFIN([]byte) error  { w.fin++; return nil }
func (w *frameCollector) BufferedAmount() uint64 { return 0 }

func TestWriteFullHandlesPartialWrites(t *testing.T) {
	w := &shortWriter{limit: 3}
	want := []byte("partial writes must not truncate")
	if err := WriteFull(w, want); err != nil {
		t.Fatalf("WriteFull: %v", err)
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("wrote %q, want %q", w.Bytes(), want)
	}
}

func TestPumpIsBinaryTransparentAcrossFrames(t *testing.T) {
	want := make([]byte, FrameSize*70+137)
	for i := range want {
		want[i] = byte(i * 31)
	}
	dst := &frameCollector{}
	n, err := Pump(context.Background(), bytes.NewReader(want), dst)
	if err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if n != int64(len(want)) || !bytes.Equal(dst.dat.Bytes(), want) {
		t.Fatalf("pumped %d bytes with matching=%v, want %d", n, bytes.Equal(dst.dat.Bytes(), want), len(want))
	}
	if dst.fin != 1 {
		t.Fatalf("FIN count = %d, want 1", dst.fin)
	}
}

func TestWriteFullRejectsNoProgress(t *testing.T) {
	err := WriteFull(writerFunc(func([]byte) (int, error) { return 0, nil }), []byte("x"))
	if err != io.ErrNoProgress {
		t.Fatalf("error = %v, want %v", err, io.ErrNoProgress)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
