package largebody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

func TestIndexRangesAndTrailingModel(t *testing.T) {
	raw := ` {"messages": [ {"role":"user","content":"hi😀"}, {"role":"assistant","content":[{"type":"text","text":"ok"}]} ], "input":"plain", "model":"mod\u0065l", "stream":true} `
	doc, err := Index(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Model != "model" || !doc.Stream || len(doc.Arrays["messages"]) != 2 {
		t.Fatalf("bad index: %+v", doc)
	}
	span := doc.Arrays["messages"][1]
	part := raw[span.Offset : span.Offset+span.Length]
	var msg map[string]any
	if err = json.Unmarshal([]byte(part), &msg); err != nil || msg["role"] != "assistant" {
		t.Fatalf("bad span %q %v", part, err)
	}
	inner, err := IndexObject(context.Background(), strings.NewReader(part))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.Arrays["content"]) != 1 {
		t.Fatalf("missing content blocks %+v", inner.Arrays)
	}
	role := inner.Fields["role"]
	if part[role.Offset:role.Offset+role.Length] != `"assistant"` {
		t.Fatal("inner offsets are not relative")
	}
}

func TestIndexRejectsMalformedAndAmbiguousJSON(t *testing.T) {
	invalid := []string{
		``, `[]`, `null`, `{`, `{} {}`, `{"model":"a","model":"b"}`, `{"model":"a","\u006dodel":"b"}`,
		`{"m":01}`, `{"m":1.}`, `{"m":+1}`, `{"m":.1}`, `{"m":1e}`, `{"m":1e+}`, `{"m":-}`, `{"m":tru}`,
		`{"m":[1,]}`, `{"m":{"x":1,}}`, `{"m":"\x00"}`, `{"m":"\u123"}`, `{"m":"\ud800"}`, `{"m":"\udc00"}`,
		"{\"m\":\"a\nb\"}", string([]byte{'{', '"', 'm', '"', ':', '"', 0xff, '"', '}'}),
	}
	for _, raw := range invalid {
		if _, err := Index(context.Background(), strings.NewReader(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	valid := []string{`{}`, `{"m":-0}`, `{"m":0.1e-2}`, `{"m":true,"n":false,"p":null}`, `{"m":"\ud83d\ude00"}`, `{"m":[{},[],1,"s"]}`}
	for _, raw := range valid {
		if _, err := Index(context.Background(), strings.NewReader(raw)); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}

func TestIndexBoundsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Index(ctx, strings.NewReader(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Index(context.Background(), strings.NewReader(`{"x":`+strings.Repeat("[", MaxDepth+1)+`0`+strings.Repeat("]", MaxDepth+1)+`}`)); !errors.Is(err, ErrStructureLimit) {
		t.Fatalf("depth: %v", err)
	}
	var raw strings.Builder
	raw.WriteString(`{"messages":[`)
	for i := 0; i < MaxArrayItems+1; i++ {
		if i > 0 {
			raw.WriteByte(',')
		}
		raw.WriteString(`{}`)
	}
	raw.WriteString(`]}`)
	if _, err := Index(context.Background(), strings.NewReader(raw.String())); !errors.Is(err, ErrStructureLimit) {
		t.Fatalf("items: %v", err)
	}
}

type chunkReader struct{ r io.Reader }

func (r chunkReader) Read(p []byte) (int, error) { return r.r.Read(p[:min(1, len(p))]) }

func TestIndexHandlesFragmentedUTF8AndEscapes(t *testing.T) {
	raw := `{"contents":[{"parts":[{"text":"中😀\uD83D\uDE00\"\\\n"}]}],"model":"末尾"}`
	doc, err := Index(context.Background(), chunkReader{strings.NewReader(raw)})
	if err != nil || doc.Model != "末尾" || len(doc.Arrays["contents"]) != 1 {
		t.Fatalf("%+v %v", doc, err)
	}
}

func TestIndexGiantStringWithoutMaterializingIt(t *testing.T) {
	// The source has no huge in-memory backing slice. The scanner only needs
	// its fixed reader buffer plus the two message spans.
	const size = 20_000_000
	reader := io.MultiReader(strings.NewReader(`{"messages":[{"role":"tool","content":"`), &fillReader{remaining: size}, strings.NewReader(`"},{"role":"user","content":"next"}],"model":"m"}`))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	doc, err := Index(context.Background(), reader)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Model != "m" || len(doc.Arrays["messages"]) != 2 || doc.Arrays["messages"][0].Length < size {
		t.Fatalf("bad giant index %+v", doc)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("scanner allocated %d bytes for generated %d-byte string", allocated, size)
	}
}

func TestIndexPreservesSourceErrors(t *testing.T) {
	for _, prefix := range []string{`{`, `{"x":`, `{"x":[`, `{"x":1`, `{"x":true`, `{"x":"str`} {
		_, err := Index(context.Background(), io.MultiReader(strings.NewReader(prefix), brokenReader{}))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("prefix %q lost source error: %v", prefix, err)
		}
	}
}

func FuzzIndexAgreesOnAcceptedJSON(f *testing.F) {
	for _, raw := range []string{`{}`, `{"messages":[{"content":"中"}]}`, `{"x":-1.25e+2}`, `{"m":"\uD83D\uDE00"}`} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			return
		}
		_, err := Index(context.Background(), bytes.NewReader(raw))
		if err == nil && !json.Valid(raw) {
			t.Fatalf("accepted invalid JSON: %q", raw)
		}
	})
}

func BenchmarkIndexGeneratedBody(b *testing.B) {
	const prefix = `{"messages":[{"role":"tool","content":"`
	const suffix = `"},{"role":"user","content":"next"}],"model":"m"}`
	for _, size := range []int64{1_000_000, 10_000_000, 100_000_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.SetBytes(size)
			b.ReportAllocs()
			for b.Loop() {
				r := io.MultiReader(strings.NewReader(prefix), &fillReader{remaining: size - int64(len(prefix)+len(suffix))}, strings.NewReader(suffix))
				if _, err := Index(context.Background(), r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
