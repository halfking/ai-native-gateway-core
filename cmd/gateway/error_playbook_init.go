package main

// 错误处理手册（Error Handling Playbook）的装配面。
//
//	错误命中手册条目 ⇒ 标准模式按预定方案执行；未知错误 ⇒ PI 智能体
//	log+上下文分析 → 根因 → 方案 → 门控执行一次修正 → 落新条目。
//	方案形态三种：script（白名单动作步骤）/ api（管理面调用闭集）/ prompt
//	（AI 执行提示词）。
//
// ★ 与 PI 智能体（pi_agent_init.go）同款装配纪律：
//
//	① 复用正典分析端点 LLM_GATEWAY_ANALYSIS_BASE_URL / _API_KEY +
//	  validateAnalysisEndpoint（外部分析端点 ⇒ 无回环递归面）；
//	② 默认全部关闭（settings error_playbook.enabled=false；
//	  env LLM_GATEWAY_ERROR_PLAYBOOK_ENABLED）⇒ 未开启时 worker 是惰性壳，
//	  管理面（手册 CRUD/手动执行/手动分析）仍然可用；
//	③ 组件进程内持有（errorPlaybookEngineGlobal），管理 API 注册点
//	  （main.go 尾段）经它取用。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// errorPlaybookEngineGlobal 供管理 API 注册时取用（与 piErrorCollectorGlobal
// 同款形态：构造点早于注册点，只能进程内传递）。
var (
	errorPlaybookEngineGlobal  *errorplaybook.Engine
	errorPlaybookStoreGlobal   *errorplaybook.SQLStore
	errorPlaybookActionsGlobal errorplaybook.ActionRegistry
)

// loadErrorPlaybookConfig 从 env + settings 读装配配置。
//
//	★ settings spec（error_playbook.*）自带 env 回退（DB settings_kv → env →
//	default）⇒ 这里只读 settings 一层就够；enabled=false 时其余值只是
//	缺省形状，worker 每轮重读 ⇒ 热加载即时生效。
func loadErrorPlaybookConfig() errorplaybook.Config {
	cfg := errorplaybook.DefaultConfig()
	cfg.Enabled = settings.GetPlatformBool("error_playbook.enabled", false)
	cfg.AutoAnalyze = settings.GetPlatformBool("error_playbook.auto_analyze", true)
	cfg.AutoExecute = settings.GetPlatformBool("error_playbook.auto_execute", true)
	if v := settings.GetPlatformInt("error_playbook.kind_threshold", 3); v > 0 {
		cfg.KindThreshold = v
	}
	if v := settings.GetPlatformInt("error_playbook.execution_cooldown_minutes", 30); v > 0 {
		cfg.ExecutionCooldown = time.Duration(v) * time.Minute
	}
	cfg.AnalyzeDedup = 24 * time.Hour
	cfg.RefineCooldown = 2 * time.Hour
	return cfg
}

// playbookInterval 返回 worker 轮询间隔（spec error_playbook.analyze_interval_seconds）。
func playbookInterval() time.Duration {
	sec := settings.GetPlatformInt("error_playbook.analyze_interval_seconds", 300)
	if sec < 30 {
		sec = 30
	}
	if sec > 3600 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}

// playbookLLMTimeout 返回 LLM 调用超时（spec error_playbook.llm_timeout_seconds，5..600）。
func playbookLLMTimeout() time.Duration {
	sec := settings.GetPlatformInt("error_playbook.llm_timeout_seconds", 150)
	if sec < 5 {
		sec = 5
	}
	if sec > 600 {
		sec = 600
	}
	return time.Duration(sec) * time.Second
}

// initErrorPlaybook 构造手册组件（store 永远构造；worker/engine/LLM 按配置）。
//
//	返回值可能部分为 nil：
//	  - store/actions 为 nil ⇒ 没有 DB（整链不可用，管理面不注册）。
//	  - engine 为 nil ⇒ LLM 未配置（手动分析 503；标准匹配执行仍可用）。
func initErrorPlaybook(ctx context.Context, sqlDB *sql.DB) (
	*errorplaybook.SQLStore, errorplaybook.ActionRegistry, *errorplaybook.Engine, *errorplaybook.Worker,
) {
	if sqlDB == nil {
		return nil, nil, nil, nil
	}

	store := errorplaybook.NewStore(sqlDB)

	// call_admin_api 扩展路由：把装配侧已有的能力登记成闭集路由。
	extraRoutes := []errorplaybook.AdminRoute{}
	if piErrorAnalyzerGlobal != nil {
		analyzer := piErrorAnalyzerGlobal
		extraRoutes = append(extraRoutes, errorplaybook.AdminRoute{
			Method: "POST",
			Path:   "/api/admin/pi-analysis/trigger",
			Handler: func(ctx context.Context, body json.RawMessage) (int, string, error) {
				batch := 10
				if len(body) > 0 {
					var req struct {
						BatchSize int `json:"batch_size"`
					}
					if err := json.Unmarshal(body, &req); err == nil && req.BatchSize > 0 && req.BatchSize <= 100 {
						batch = req.BatchSize
					}
				}
				n, err := analyzer.ProcessBatchN(ctx, batch)
				if err != nil {
					return 500, "", err
				}
				return 200, fmt.Sprintf("processed %d items", n), nil
			},
		})
	}
	actions := errorplaybook.NewBuiltinActions(sqlDB, extraRoutes)

	// LLM 客户端：复用正典分析端点（与 PI 智能体同一个端点/密钥面）。
	var engine *errorplaybook.Engine
	if llm := newPlaybookLLMClient(); llm != nil {
		engine = errorplaybook.NewEngine(store, actions, llm, errorplaybook.NewLogContext(sqlDB))
	} else {
		engine = errorplaybook.NewEngine(store, actions, nil, errorplaybook.NewLogContext(sqlDB))
		slog.Warn("error_playbook: LLM analysis endpoint not configured; unknown-error analysis disabled " +
			"(LLM_GATEWAY_ANALYSIS_BASE_URL / LLM_GATEWAY_ANALYSIS_API_KEY)")
	}

	worker := errorplaybook.NewWorker(engine, errorplaybook.NewSQLSource(sqlDB), loadErrorPlaybookConfig, playbookInterval())
	worker.Start(ctx)

	slog.Info("error_playbook: components constructed",
		"automation_enabled", loadErrorPlaybookConfig().Enabled,
		"llm_wired", engine.HasLLM(),
		"admin_api_routes", len(extraRoutes)+4)
	return store, actions, engine, worker
}

// newPlaybookLLMClient 构造手册分析用的 LLM 客户端（未配置 ⇒ nil）。
//
//	★ 与 PI 智能体同端点同密钥（LLM_GATEWAY_ANALYSIS_*），模型独立旋钮
//	LLM_GATEWAY_ERROR_PLAYBOOK_MODEL（缺省回落 PI 分析模型缺省 gpt-4o-mini）。
func newPlaybookLLMClient() *promptinjection.GatewayLLMClient {
	baseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_BASE_URL"))
	}
	apiKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_API_KEY"))
	}
	if baseURL == "" || apiKey == "" {
		return nil
	}
	allowInsecureLocal := os.Getenv("LLM_GATEWAY_ANALYSIS_ALLOW_INSECURE_LOCAL") == "true"
	validatedURL, err := validateAnalysisEndpoint(baseURL, allowInsecureLocal)
	if err != nil {
		slog.Warn("error_playbook: analysis endpoint rejected; LLM analysis disabled", "error", err)
		return nil
	}
	model := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ERROR_PLAYBOOK_MODEL"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("LLMGatewayPIErrorAnalysisModel"))
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	client := promptinjection.NewGatewayLLMClient(model, "/chat/completions", apiKey, validatedURL)
	client.SetTimeout(playbookLLMTimeout())
	return client
}
