// Package sessionforensics - service.go
//
// 高层协调器 — 对外暴露一个 entry point，包装 Export / Replay / Summarize，
// 让运维平台（admin web）、CLI、CI 工具都只需要调 Service.DownloadAndReplay
// / Service.Summarize 这样的 method。
//
// 这是 sessionforensics 包推荐的"日常用法"：
//
//	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{
//	    DB:            pgPool,   // 直接的 pgxpool.Pool 或者 mock
//	    HTTPBaseURL:   "https://llm.itestu.cn",
//	    Bearer:        jwtToken,
//	    LocalStoreDir: "tests/session_replay/sessions", // 留作可选
//	})
//
//	pack, _ := svc.DownloadSession(ctx, "gw_xxxxx", "default")
//	report := svc.Replay(ctx, pack, sessionforensics.ReplayOptions{...})
//	summary := svc.Summarize(ctx, pack, sessionforensics.SummarizeOptions{})
package sessionforensics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// ServiceConfig 是 Service 的配置项。
type ServiceConfig struct {
	DB Store // pgxpool.Pool wrapped by PgxStore（也可以传 nil 走 HTTP-only 路径）

	HTTPClient  HTTPClient
	HTTPBaseURL string
	Bearer      string
	Cookie      string

	// LocalStoreDir 把下载的 pack 落盘到本地（CI / 调试用），空 = 不落盘。
	LocalStoreDir string

	// DefaultReplay 当用户没传 ReplayOptions 时使用的默认参数
	DefaultReplay ReplayOptions
}

// SummarizeArgs 是 Service.Summarize 的可选项（与 sessionsummary 子包
// 的 SummarizeOptions 区分；额外加 Persist 字段控制是否写回 DB）。
type SummarizeArgs struct {
	TenantID             string
	FirstMessageOverride string
	// ForceSource "llm" | "fallback" | "preview" | "" (auto)
	ForceSource string
	// Rolling true 时若 Redis 已有摘要则走 GenerateRollingSummary
	Rolling bool
	// Persist 是否写回 DB。nil / true = 写，false = 不写
	Persist *bool
}

// Service 聚合 Exporter + Client + Replayer + Summarizer。
type Service struct {
	cfg      ServiceConfig
	exporter *Exporter
	client   *Client
	replayer *Replayer
	// 2026-07-27 concurrency fix: summarize 之前是裸字段，AutoSummaryHook
	// 会在请求路径上调 SetSummarizer，而 worker goroutine 同时在读它 →
	// 数据竞争。改成 atomic.Pointer，读写都无锁且安全。
	summarize atomic.Pointer[Summarizer]
}

// NewService 把 config 包装成可用的 service。
func NewService(cfg ServiceConfig) *Service {
	var exp *Exporter
	if cfg.DB != nil {
		exp = NewExporterWithStore(cfg.DB)
	} else {
		exp = &Exporter{store: nil}
	}
	cli := &Client{
		BaseURL:    cfg.HTTPBaseURL,
		Bearer:     cfg.Bearer,
		Cookie:     cfg.Cookie,
		HTTPClient: cfg.HTTPClient,
		UserAgent:  "sessionforensics/1.0",
	}
	if cli.HTTPClient == nil {
		cli.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	svc := &Service{
		cfg:      cfg,
		exporter: exp,
		client:   cli,
		replayer: NewReplayer(),
	}
	svc.summarize.Store(NewSummarizer(nil))
	return svc
}

// SetSummarizer 让运维平台把自己的 sessionsummary.Summarizer 注入。
func (s *Service) SetSummarizer(sm *Summarizer) {
	if sm != nil {
		s.summarize.Store(sm)
	}
}

// DownloadSession 按优先级从本地 DB → 远程 admin 拉取会话包。
//
// 1. 如果 cfg.DB 不为空且 store 不报错：从本地 store 拉
// 2. 否则：调 cfg.HTTPBaseURL 上的 /api/admin/session-export 拉
//
// 第二种命中时会把 pack 落盘到 cfg.LocalStoreDir (非空时)。
func (s *Service) DownloadSession(ctx context.Context, sessionID, tenantID string) (*SessionPack, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("sessionforensics: missing session_id")
	}
	if tenantID == "" {
		tenantID = "default"
	}

	if s.exporter != nil && s.exporter.store != nil {
		pack, err := s.exporter.ExportSession(ctx, sessionID, tenantID)
		if err == nil {
			s.maybePersist(pack)
			return pack, nil
		}
		if err != ErrSessionNotFound {
			return nil, err
		}
	}

	if s.client.BaseURL == "" {
		return nil, ErrSessionNotFound
	}
	pack, err := s.client.Download(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	s.maybePersist(pack)
	return pack, nil
}

// Replay 跑 pack 的回放并返回 *ReplayReport。
func (s *Service) Replay(ctx context.Context, pack *SessionPack, opt *ReplayOptions) *ReplayReport {
	if opt == nil {
		opt = &s.cfg.DefaultReplay
	}
	return s.replayer.Replay(ctx, pack, *opt)
}

// Summarize 对 pack 计算 title + summary。
// Persist 字段控制是否写回 DB（默认 true）。
func (s *Service) Summarize(ctx context.Context, pack *SessionPack, args SummarizeArgs) (*SummaryResult, error) {
	if pack == nil {
		return nil, fmt.Errorf("sessionforensics: nil pack")
	}
	if args.TenantID == "" {
		args.TenantID = "default"
	}
	firstMsg := args.FirstMessageOverride
	if firstMsg == "" {
		firstMsg = s.extractFirstReadableMessage(pack, "")
	}
	summarizer := s.summarize.Load()
	if summarizer == nil {
		summarizer = NewSummarizer(nil)
	}
	res, err := summarizer.Summarize(ctx, pack.SessionMeta.ID,
		SummarizeOptions{
			TenantID:             args.TenantID,
			FirstMessageOverride: firstMsg,
			ForceSource:          args.ForceSource,
			Rolling:              args.Rolling,
		})
	if err != nil {
		return nil, err
	}
	persist := args.Persist == nil || *args.Persist
	if persist && s.exporter != nil && s.exporter.store != nil {
		if uerr := s.exporter.UpsertSummary(ctx, pack.SessionMeta.ID, *res); uerr != nil {
			res.Error = fmt.Sprintf("upsert failed: %v", uerr)
		}
	}
	return res, nil
}

// ListRecentSessions 列最近活跃的 session（运维平台 UI）。
func (s *Service) ListRecentSessions(ctx context.Context, tenantID string, limit int) ([]SessionAudit, error) {
	if s.exporter == nil || s.exporter.store == nil {
		return nil, ErrorDBUnavailable
	}
	return s.exporter.ListRecentSessions(ctx, tenantID, limit)
}

// maybePersist 落盘 pack 到 cfg.LocalStoreDir。
func (s *Service) maybePersist(pack *SessionPack) {
	if s.cfg.LocalStoreDir == "" || pack == nil || pack.SessionMeta.ID == "" {
		return
	}
	if err := os.MkdirAll(s.cfg.LocalStoreDir, 0o755); err != nil {
		return
	}
	safe := sanitizeFileName(pack.SessionMeta.ID)
	fp := filepath.Join(s.cfg.LocalStoreDir, fmt.Sprintf("session_%s.json", safe))
	b, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(fp, b, 0o644)
}

// firstUserOrChatContent 优先用 args.FirstMessageOverride；否则尝试从
// pack.Messages 里挑首个 user role 的可读内容（解析 chat body）。
func (s *Service) extractFirstReadableMessage(pack *SessionPack, override string) string {
	if override != "" {
		return override
	}
	// 1. 直接从 Content 解析（适用于 pack.Content 是 chat body 字符串）
	for _, m := range pack.Messages {
		if u := ExtractFirstUserMessage([]byte(m.Content)); u != "" {
			return u
		}
	}
	// 2. fallback：如果 Content 已经是可读文本
	for _, m := range pack.Messages {
		if m.Role == "user" && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

func sanitizeFileName(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range s {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			out = append(out, '_')
		default:
			out = append(out, byte(c))
		}
	}
	return string(out)
}
