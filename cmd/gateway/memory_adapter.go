package main

import (
	"context"
	"os"

	"github.com/kaixuan/llm-gateway-go/_to-be-deprecated/memora"
	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// legacyMemoryServices wraps the deprecated concrete memora client/sink and
// exposes only the live memory.Reader / memory.Writer surfaces used by the
// cut-over gateway runtime.
type legacyMemoryServices struct {
	client *memora.Client
	sink   *memora.Sink
}

func newLegacyMemoryServices(client *memora.Client, sink *memora.Sink) *legacyMemoryServices {
	return &legacyMemoryServices{client: client, sink: sink}
}

func newLegacyMemoryServicesFromEnv(baseURL string) *legacyMemoryServices {
	if baseURL == "" {
		return nil
	}
	client := memora.NewClient(memora.ClientConfig{
		BaseURL:            baseURL,
		APIKey:             os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
		SmartSearchBaseURL: os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL"),
		SmartSearchAPIKey:  os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_API_KEY"),
	})
	sink := memora.NewSink(client, 2, 2048)
	return newLegacyMemoryServices(client, sink)
}

func (s *legacyMemoryServices) Reader() memory.Reader {
	if s == nil || s.client == nil {
		return nil
	}
	return legacyMemoraReader{c: s.client}
}

func (s *legacyMemoryServices) Writer() memory.Writer {
	if s == nil || s.sink == nil {
		return nil
	}
	return legacyMemoraWriter{s: s.sink}
}

func (s *legacyMemoryServices) AdminClient() interface {
	Disabled() bool
	Ping(ctx context.Context) error
	BaseURL() string
	AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error
	Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error)
} {
	if s == nil || s.client == nil {
		return nil
	}
	return legacyMemoraReader{c: s.client}
}

func (s *legacyMemoryServices) AdminSink() interface {
	Stats() memory.Stats
	Pause()
	Resume()
} {
	if s == nil || s.sink == nil {
		return nil
	}
	return legacyMemoraWriter{s: s.sink}
}

func (s *legacyMemoryServices) Start() {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.Start()
}

func (s *legacyMemoryServices) Stop(ctx context.Context) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.Stop(ctx)
}
