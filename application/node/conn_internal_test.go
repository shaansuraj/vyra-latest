package node

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type shortWriteBuffer struct {
	bytes.Buffer
	maxWrite int
}

func (w *shortWriteBuffer) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		p = p[:w.maxWrite]
	}
	return w.Buffer.Write(p)
}

type zeroWriteBuffer struct{}

func (zeroWriteBuffer) Write([]byte) (int, error) {
	return 0, nil
}

func TestWriteFullRetriesShortWrites(t *testing.T) {
	var dst shortWriteBuffer
	dst.maxWrite = 3

	payload := []byte("rtsp-over-bridge-payload")
	if err := writeFull(&dst, payload); err != nil {
		t.Fatalf("writeFull failed: %v", err)
	}

	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("payload mismatch: got %q want %q", dst.Bytes(), payload)
	}
}

func TestWriteFullRejectsZeroByteWrite(t *testing.T) {
	err := writeFull(zeroWriteBuffer{}, []byte("payload"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected io.ErrShortWrite, got %v", err)
	}
}
