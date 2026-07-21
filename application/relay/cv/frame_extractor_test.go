package cv

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWriteAccessUnitNALsLengthPrefixed(t *testing.T) {
	payload := appendLengthPrefixedNALs([][]byte{
		{0x67, 0x01, 0x02},
		{0x68, 0x03},
	})

	got, err := collectAccessUnitNALs("H264", payload)
	if err != nil {
		t.Fatalf("writeAccessUnitNALs returned error: %v", err)
	}
	assertNALs(t, got, [][]byte{
		{0x67, 0x01, 0x02},
		{0x68, 0x03},
	})
}

func TestWriteAccessUnitNALsAnnexB(t *testing.T) {
	payload := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x01, 0x02,
		0x00, 0x00, 0x01, 0x68, 0x03,
	}

	got, err := collectAccessUnitNALs("H264", payload)
	if err != nil {
		t.Fatalf("writeAccessUnitNALs returned error: %v", err)
	}
	assertNALs(t, got, [][]byte{
		{0x67, 0x01, 0x02},
		{0x68, 0x03},
	})
}

func TestWriteAccessUnitNALsRejectsMalformedLengthPrefix(t *testing.T) {
	payload := []byte{0x00, 0x00, 0x00, 0x08, 0x65, 0x01}

	if _, err := collectAccessUnitNALs("H264", payload); err == nil {
		t.Fatal("expected malformed length-prefixed payload to return an error")
	}
}

func TestWriteAccessUnitNALsKeepsRawH264NAL(t *testing.T) {
	payload := []byte{0x65, 0x88, 0x84, 0x21, 0xA0, 0x0F}

	got, err := collectAccessUnitNALs("H264", payload)
	if err != nil {
		t.Fatalf("writeAccessUnitNALs returned error: %v", err)
	}
	assertNALs(t, got, [][]byte{payload})
}

func TestWriteAccessUnitNALsDoesNotPartiallyEmitMalformedAVCC(t *testing.T) {
	payload := appendLengthPrefixedNALs([][]byte{{0x67, 0x01, 0x02}})
	payload = append(payload, 0x00, 0x00)

	got, err := collectAccessUnitNALs("H264", payload)
	if err == nil {
		t.Fatal("expected malformed length-prefixed payload to return an error")
	}
	if len(got) != 0 {
		t.Fatalf("expected no partial NAL emission, got %#v", got)
	}
}

func TestWriteAccessUnitNALsDoesNotPartiallyEmitInvalidNALHeader(t *testing.T) {
	payload := appendLengthPrefixedNALs([][]byte{
		{0x67, 0x01, 0x02},
		{0x80, 0x03},
	})

	got, err := collectAccessUnitNALs("H264", payload)
	if err == nil {
		t.Fatal("expected invalid NAL header to return an error")
	}
	if len(got) != 0 {
		t.Fatalf("expected no partial NAL emission, got %#v", got)
	}
}

func TestWriteAllRetriesShortWrites(t *testing.T) {
	var dst shortWriter
	dst.maxWrite = 2

	payload := []byte("complete frame bytes")
	if err := writeAll(&dst, payload); err != nil {
		t.Fatalf("writeAll returned error: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("writeAll wrote %q, want %q", dst.Bytes(), payload)
	}
}

func appendLengthPrefixedNALs(nals [][]byte) []byte {
	var out []byte
	for _, nal := range nals {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(nal)))
		out = append(out, size[:]...)
		out = append(out, nal...)
	}
	return out
}

func collectAccessUnitNALs(codecName string, payload []byte) ([][]byte, error) {
	var got [][]byte
	err := writeAccessUnitNALs(codecName, payload, func(nal []byte) {
		got = append(got, append([]byte(nil), nal...))
	})
	return got, err
}

func assertNALs(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d NALs, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("NAL %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

type shortWriter struct {
	bytes.Buffer
	maxWrite int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		p = p[:w.maxWrite]
	}
	return w.Buffer.Write(p)
}
