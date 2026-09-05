package preprocess

import (
	"bytes"
	"compress/gzip"
	"io"
)

func isZeroHash(h [32]byte) bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return b // best effort: fall back to plaintext-compatible bytes
	}
	if err := w.Close(); err != nil {
		return b
	}
	return buf.Bytes()
}

func gunzipBytes(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
