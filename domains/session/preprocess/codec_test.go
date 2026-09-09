package preprocess

import (
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreCodecDefaultsToGzip(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) { o.CompressSanitized = true })
	assert.Equal(t, encodingGZIP, store.opts.codec())
	payload := []byte(strings.Repeat(`{"role":"user","content":"会话载荷"}`, 20))
	data, tag, err := store.encodePayload("tenant-c", "sess-c", ArtifactSanitized, payload)
	require.NoError(t, err)
	assert.Equal(t, encodingGZIP, tag)
	assert.Equal(t, []byte{0x1f, 0x8b}, data[:2])
}

func TestStoreCodecZstdRoundTrip(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) {
		o.CompressSanitized, o.CompressCompressed, o.Codec = true, true, encodingZSTD
	})
	payload := []byte(strings.Repeat(`{"role":"assistant","content":"根因分析结论……"}`, 20))
	for _, kind := range []ArtifactKind{ArtifactSanitized, ArtifactCompressed} {
		data, tag, err := store.encodePayload("tenant-z", "sess-z", kind, payload)
		require.NoError(t, err)
		assert.Equal(t, encodingZSTD, tag)
		got, err := store.decodePayload("tenant-z", "sess-z", kind, tag, data)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	}
}

func TestStoreDecodeLegacyGzipWithZstdCodec(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) { o.CompressSanitized, o.Codec = true, encodingZSTD })
	payload := []byte("legacy gzip payload " + strings.Repeat("x", 100))
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	got, err := store.decodePayload("tenant-z", "sess-z", ArtifactSanitized, encodingGZIP, buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	_, err = store.decodePayload("tenant-z", "sess-z", ArtifactSanitized, "lz4", []byte("x"))
	assert.ErrorIs(t, err, ErrArtifactCorrupted)
}

func TestStoreZstdPutGetRoundTrip(t *testing.T) {
	store, mr := newTestStore(t, func(o *StoreOptions) { o.CompressSanitized, o.Codec = true, encodingZSTD })
	ctx := context.Background()
	payload := []byte("zstd artifact payload " + strings.Repeat("repeat ", 20))
	ok, err := store.PutCAS(ctx, sampleArtifact("t-zstd", "s-zstd", ArtifactSanitized, "", payload), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)
	key := store.artifactKey("t-zstd", "s-zstd", ArtifactSanitized, "default")
	require.Equal(t, encodingZSTD, mr.HGet(key, fieldEnc))
	got, found, err := store.Get(ctx, "t-zstd", "s-zstd", ArtifactSanitized, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, payload, got.Payload)
}
