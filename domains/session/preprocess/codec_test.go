package preprocess

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreCodecDefaultsToGzip 零值 Codec 必须保持历史行为（gzip 标签），
// 保证未显式配置的既有部署语义不变。
func TestStoreCodecDefaultsToGzip(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) {
		o.CompressSanitized = true
	})
	assert.Equal(t, encodingGZIP, store.opts.codec())

	payload := []byte(strings.Repeat(`{"role":"user","content":"会话载荷"}`, 20))
	data, tag, err := store.encodePayload("tenant-c", "sess-c", ArtifactSanitized, payload)
	require.NoError(t, err)
	assert.Equal(t, encodingGZIP, tag)

	// gzip 魔数 0x1F 0x8B
	assert.Equal(t, []byte{0x1f, 0x8b}, data[:2])
}

// TestStoreCodecZstdRoundTrip zstd 编码开关下，sanitize/compressed 层新写
// 载荷带 zstd 标签，Get 读回与原文一致。
func TestStoreCodecZstdRoundTrip(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) {
		o.CompressSanitized = true
		o.CompressCompressed = true
		o.Codec = encodingZSTD
	})
	assert.Equal(t, encodingZSTD, store.opts.codec())

	payload := []byte(strings.Repeat(`{"role":"assistant","content":"根因分析结论……"}`, 20))
	for _, kind := range []ArtifactKind{ArtifactSanitized, ArtifactCompressed} {
		data, tag, err := store.encodePayload("tenant-z", "sess-z", kind, payload)
		require.NoError(t, err, "kind=%s", kind)
		assert.Equal(t, encodingZSTD, tag, "kind=%s", kind)

		got, err := store.decodePayload("tenant-z", "sess-z", kind, tag, data)
		require.NoError(t, err, "kind=%s", kind)
		assert.Equal(t, payload, got, "kind=%s", kind)
	}
}

// TestStoreDecodeLegacyGzipWithZstdCodec 灰度核心保证：Codec 切到 zstd 后，
// 历史 gzip 标签载荷必须照常可读（混存并存，读端按标记分发）。
func TestStoreDecodeLegacyGzipWithZstdCodec(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) {
		o.CompressSanitized = true
		o.Codec = encodingZSTD
	})

	payload := []byte("legacy gzip payload " + strings.Repeat("x", 100))
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	got, err := store.decodePayload("tenant-z", "sess-z", ArtifactSanitized, encodingGZIP, buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// 未知编码仍然报完整性错误（不静默吞掉）
	_, err = store.decodePayload("tenant-z", "sess-z", ArtifactSanitized, "lz4", []byte("x"))
	assert.ErrorIs(t, err, ErrArtifactCorrupted)
}
