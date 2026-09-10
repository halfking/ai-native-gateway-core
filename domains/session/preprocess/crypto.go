package preprocess

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

func isZeroHash(h [32]byte) bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// zstd 进程级单例（nil 流构造 = 仅承载压缩参数，EncodeAll/DecodeAll 无状态
// 且并发安全），避免每次调用重建窗口内存。与 storage/file 的选择一致：
// SpeedDefault ≈ CLI -3，并发 1（载荷小，让请求级并行公平共享 CPU）。
var (
	zstdEncOnce sync.Once
	zstdEnc     *zstd.Encoder
	zstdEncErr  error
	zstdDecOnce sync.Once
	zstdDec     *zstd.Decoder
	zstdDecErr  error
)

func zstdBytes(b []byte) ([]byte, error) {
	zstdEncOnce.Do(func() {
		zstdEnc, zstdEncErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1))
	})
	if zstdEncErr != nil {
		return nil, fmt.Errorf("zstd encoder unavailable: %w", zstdEncErr)
	}
	return zstdEnc.EncodeAll(b, nil), nil
}

func unzstdBytes(b []byte) ([]byte, error) {
	zstdDecOnce.Do(func() {
		zstdDec, zstdDecErr = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	})
	if zstdDecErr != nil {
		return nil, fmt.Errorf("zstd decoder unavailable: %w", zstdDecErr)
	}
	return zstdDec.DecodeAll(b, nil)
}
