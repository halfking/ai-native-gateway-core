package ir

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestToolArgumentsAssemblerValidFragments(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "object", chunks: []string{`{"name":`, `"Ada"}`}, want: `{"name":"Ada"}`},
		{name: "array", chunks: []string{`[1,`, `2,3]`}, want: `[1,2,3]`},
		{name: "nested", chunks: []string{`{"items":[`, `{"ok":true}]}`}, want: `{"items":[{"ok":true}]}`},
		{name: "unicode", chunks: []string{`{"城市":"东`, `京"}`}, want: `{"城市":"东京"}`},
		{name: "escaped", chunks: []string{`{"text":"a\\`, `\"b"}`}, want: `{"text":"a\\\"b"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewToolArgumentsAssembler()
			for _, chunk := range tt.chunks {
				if err := a.Append(chunk); err != nil {
					t.Fatalf("Append: %v", err)
				}
			}
			got, reason, err := a.Finalize()
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if got != tt.want || reason != "" || !json.Valid([]byte(got)) {
				t.Fatalf("Finalize = (%q, %q), want (%q, empty); valid=%v", got, reason, tt.want, json.Valid([]byte(got)))
			}
		})
	}
}

func TestToolArgumentsAssemblerRepairsSafeTruncation(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		reason string
	}{
		{name: "object close", input: `{"city":"Tokyo"`, want: `{"city":"Tokyo"}`, reason: "no_brace_close"},
		{name: "array close", input: `{"values":[1,2`, want: `{"values":[1,2]}`, reason: "no_brace_close"},
		{name: "string and object close", input: `{"city":"Tokyo`, want: `{"city":"Tokyo"}`, reason: "unescaped_string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewToolArgumentsAssembler()
			_ = a.Append(tt.input)
			got, reason, err := a.Finalize()
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if got != tt.want || reason != tt.reason || !json.Valid([]byte(got)) {
				t.Fatalf("Finalize = (%q, %q), want (%q, %q); valid=%v", got, reason, tt.want, tt.reason, json.Valid([]byte(got)))
			}
		})
	}
}

func TestToolArgumentsAssemblerRejectsInvalidJSON(t *testing.T) {
	a := NewToolArgumentsAssembler()
	_ = a.Append(`{"city":@}`)
	got, reason, err := a.Finalize()
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("Finalize err = %v, want ErrInvalidJSON", err)
	}
	if got != "" || reason != "invalid_json" {
		t.Fatalf("Finalize = (%q, %q), want (empty, invalid_json)", got, reason)
	}
}

func TestToolArgumentsAssemblerReset(t *testing.T) {
	a := NewToolArgumentsAssembler()
	_ = a.Append(`{"stale":true`)
	a.Reset()
	_ = a.Append(`["fresh"]`)
	got, reason, err := a.Finalize()
	if err != nil || got != `["fresh"]` || reason != "" {
		t.Fatalf("Finalize after Reset = (%q, %q, %v)", got, reason, err)
	}
}

func TestToolArgumentsAssemblerConcurrentAppend(t *testing.T) {
	a := NewToolArgumentsAssembler()
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			if err := a.Append(` `); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := a.Append(`{}`); err != nil {
		t.Fatalf("Append final object: %v", err)
	}
	got, _, err := a.Finalize()
	if err != nil || !json.Valid([]byte(got)) {
		t.Fatalf("Finalize = %q, %v", got, err)
	}
}

func TestStreamChunkAnnotateArgumentsJSON(t *testing.T) {
	valid := &StreamChunk{}
	valid.AnnotateArgumentsJSON(`{"city":"Tokyo"}`)
	if valid.Quality != "verified" || valid.ArgumentsJSONReason != "" {
		t.Fatalf("valid annotation = quality %q reason %q", valid.Quality, valid.ArgumentsJSONReason)
	}

	partial := &StreamChunk{}
	partial.AnnotateArgumentsJSON(`{"city":"Tokyo"`)
	if partial.Quality != "partial" || partial.ArgumentsJSONReason != "no_brace_close" {
		t.Fatalf("partial annotation = quality %q reason %q", partial.Quality, partial.ArgumentsJSONReason)
	}

	rejected := &StreamChunk{}
	rejected.AnnotateArgumentsJSON(`{"city":@}`)
	if rejected.Quality != "rejected" || rejected.ArgumentsJSONReason != "invalid_json" {
		t.Fatalf("rejected annotation = quality %q reason %q", rejected.Quality, rejected.ArgumentsJSONReason)
	}
}
