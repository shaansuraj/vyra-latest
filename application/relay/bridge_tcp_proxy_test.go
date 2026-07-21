package relay

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type bridgeShortWriteBuffer struct {
	bytes.Buffer
	maxWrite int
}

func (w *bridgeShortWriteBuffer) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		p = p[:w.maxWrite]
	}
	return w.Buffer.Write(p)
}

type bridgeZeroWriteBuffer struct{}

func (bridgeZeroWriteBuffer) Write([]byte) (int, error) {
	return 0, nil
}

func TestBridgeWriteFullRetriesShortWrites(t *testing.T) {
	var dst bridgeShortWriteBuffer
	dst.maxWrite = 5

	payload := []byte("rtsp-interleaved-rtp-payload")
	if err := writeFull(&dst, payload); err != nil {
		t.Fatalf("writeFull failed: %v", err)
	}

	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("payload mismatch: got %q want %q", dst.Bytes(), payload)
	}
}

func TestBridgeWriteFullRejectsZeroByteWrite(t *testing.T) {
	err := writeFull(bridgeZeroWriteBuffer{}, []byte("payload"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected io.ErrShortWrite, got %v", err)
	}
}
