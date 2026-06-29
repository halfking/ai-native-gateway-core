package main

import (
	"context"
	"os"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
)

// legacyMemoryServices wraps the memora client/sink (now in domains/memory/client)
// and exposes the live memory.Reader / memory.Writer surfaces used by the
// gateway runtime.
type legacyMemoryServices struct {
	client *memclient.Client
	sink   *memclient.Sink
}

func newLegacyMemoryServices(client *memclient.Client, sink *memclient.Sink) *legacyMemoryServices {
	return &legacyMemoryServices{client: client, sink: sink}
}

func newLegacyMemoryServicesFromEnv(baseURL string) *legacyMemoryServices {
	if baseURL == "" {
		return nil
	}
	client := memclient.NewClient(memclient.ClientConfig{
		BaseURL:            baseURL,
		APIKey:             os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
		SmartSearchBaseURL: os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL"),
		SmartSearchAPIKey:  os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_API_KEY"),
	})
	sink := memclient.NewSink(client, 2, 2048)
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
