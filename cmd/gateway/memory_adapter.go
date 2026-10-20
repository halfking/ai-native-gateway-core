package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionpush"
	"github.com/kaixuan/llm-gateway-go/domains/memory"                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client" //nolint:depguard
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
		// Route overrides (2026-10-20, audit D1). Empty = memora's real
		// routes (/api/memory/add, /api/search); the MemOS-era /product/*
		// routes are reachable only through the explicit opt-in
		// LLM_GATEWAY_MEMORA_LEGACY_MEMOS_PATHS=1 (read inside the client).
		AddPath:    os.Getenv("LLM_GATEWAY_MEMORA_ADD_PATH"),
		SearchPath: os.Getenv("LLM_GATEWAY_MEMORA_SEARCH_PATH"),
		// memora's project scope is fail-closed: without X-Project-ID the
		// add/search endpoints answer 400 "project_id is required".
		// Per-call project ids (the manual extraction path puts one in
		// info["project_id"]) still win over this default.
		ProjectID: os.Getenv("LLM_GATEWAY_MEMORA_PROJECT_ID"),
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
	SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error)
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

// installSessionAnalysisPusher 装上 memora 的**会话分析投递**目标（审计 G-2）。
//
// ★ 契约权威：memora main 2a8aff3 的
//
//	memora/docs/integration/2026-10-20-session-analysis-contract.md（v1.1）。
//
// 总开关是 LLM_GATEWAY_MEMORA_SESSION_ANALYSIS_PUSH：没显式打开 ⇒ 推送器是
// **禁用态**（零 HTTP 调用、零日志）⇒ 没配 memora 的环境完全不受影响。
//
// ★ 为什么装在**进程级单点**（domains/analysis/sessionpush.Set）而不是穿
//
//	注入链：产出点分散在 admin 的到达钩子与 analysis/workers 的关闭钩子，
//	中间隔着 admin.Handler 与 v2DispatchDeps 两层 ⇒ 穿链更容易漏接；
//	而这条链是**纯副作用**（推送失败不影响任何业务结果）。
func installSessionAnalysisPusher(baseURL string) {
	pusher := memclient.SessionAnalysisPusherFromEnv(baseURL)
	if pusher.Disabled() {
		// 禁用时**显式清空**进程级目标：避免上一个（测试/热更）装上的
		// 推送器残留 —— 那会让「以为没开的功能」真的在发请求。
		sessionpush.Set(nil)
		slog.Debug("memora session-analysis push disabled",
			"base_url_set", baseURL != "")
		return
	}
	sessionpush.Set(pusher)
	slog.Info("memora session-analysis push enabled",
		"base_url", pusher.BaseURL(),
		"project_id", pusher.ProjectID())
}
