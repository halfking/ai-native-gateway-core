// codec.go — 会话内容落盘编解码器抽象（2026-09-10 zstd 引入，方案见
// ai-native-tools docs/zstd-compression-analysis-2026-09-10.md）。
//
// 历史上会话体固定 gzip（turn_N.json.gz）。工作区真实语料实测（报告 §3）：
// 同档位下 zstd 压缩比更优（71MB 源码 4.32× vs 3.77×）且压缩吞吐高一个
// 数量级（1018 vs 43 MB/s）、解压快 1.7~3 倍，因此在 gzip 之外新增 zstd。
//
// 迁移策略（读时多算法兼容）：
//   - 文件后缀即编码标识：.json.gz = gzip，.json.zst = zstd；
//   - 写路径用 store 配置的单一编码（factory 接线 config，默认 zstd，
//     env LLM_GATEWAY_BODIES_CODEC=gzip 可回退）；
//   - 读路径按「配置编码优先 + 全部已知编码兜底」依次尝试，历史 gzip
//     文件无需迁移即可继续读取。
package file

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Codec 会话内容压缩编码。
type Codec string

const (
	// CodecGzip 标准库 gzip(DEFLATE) 编码，历史默认，读写永久兼容。
	CodecGzip Codec = "gzip"

	// CodecZstd zstd 编码（klauspost/compress 纯 Go 实现，无 cgo；
	// SpeedDefault ≈ CLI -3 档）。2026-09-10 起为 lite 会话体落盘默认。
	CodecZstd Codec = "zstd"
)

// Suffix 返回该编码的轮次文件名后缀。
func (c Codec) Suffix() string {
	if c == CodecZstd {
		return ".json.zst"
	}
	return ".json.gz"
}

// ParseCodec 把配置字符串解析为 Codec。空串或未知值回落 CodecGzip，
// 保持历史行为（配置打错字不应悄悄改变落盘格式）。
func ParseCodec(s string) Codec {
	if Codec(strings.ToLower(strings.TrimSpace(s))) == CodecZstd {
		return CodecZstd
	}
	return CodecGzip
}

// AllCodecs 返回全部已知编码，读路径兜底顺序（新编码在前）。
// 新增编码时登记在此即可同时获得读兼容与 ListTurns/DeleteTurnFile
// 的双格式处理。
func AllCodecs() []Codec {
	return []Codec{CodecZstd, CodecGzip}
}

// zstd 进程级单例：klauspost 的 Encoder/Decoder 以 nil 流构造时仅承载
// 全局压缩参数，EncodeAll/DecodeAll 为无状态调用且并发安全；复用单例
// 避免每次调用重建窗口/字典内存。并发度压在 1：单条会话体较小（KB 级），
// 块内并行收益有限，让请求级并行公平共享 CPU。
var (
	zstdEncOnce sync.Once
	zstdEnc     *zstd.Encoder
	zstdEncErr  error
	zstdDecOnce sync.Once
	zstdDec     *zstd.Decoder
	zstdDecErr  error
)

func zstdEncoder() (*zstd.Encoder, error) {
	zstdEncOnce.Do(func() {
		zstdEnc, zstdEncErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1))
	})
	return zstdEnc, zstdEncErr
}

func zstdDecoder() (*zstd.Decoder, error) {
	zstdDecOnce.Do(func() {
		zstdDec, zstdDecErr = zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1))
	})
	return zstdDec, zstdDecErr
}

// gzipEncode 压缩一段字节流为完整 gzip 流（须先 Close 刷新残余字节）。
func gzipEncode(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// gunzip 解压一段 gzip 字节流；头或数据体损坏时返回带上下文的错误。
func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	defer zr.Close()

	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("gzip body: %w", err)
	}
	return raw, nil
}

// encode 按该编码压缩字节流。
func (c Codec) encode(data []byte) ([]byte, error) {
	if c == CodecZstd {
		enc, err := zstdEncoder()
		if err != nil {
			return nil, fmt.Errorf("zstd encoder: %w", err)
		}
		return enc.EncodeAll(data, nil), nil
	}
	return gzipEncode(data)
}

// decode 解压一段按该编码压缩的字节流。
func (c Codec) decode(data []byte) ([]byte, error) {
	if c == CodecZstd {
		dec, err := zstdDecoder()
		if err != nil {
			return nil, fmt.Errorf("zstd decoder: %w", err)
		}
		out, err := dec.DecodeAll(data, nil)
		if err != nil {
			return nil, fmt.Errorf("zstd body: %w", err)
		}
		return out, nil
	}
	return gunzip(data)
}
