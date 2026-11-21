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
	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
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

// PlaybookDeps 是 call_admin_api 闭集扩展路由的装配依赖（全部可选）。
//
//	★ 2026-11-21 任务 3：闭集从 1 条（pi-analysis/trigger）扩到 4 条——
//	  三条高频处置端点全部走「静态路径 + body 传参」的内部直调实现，
//	  不经过 HTTP 层（无鉴权旁路面：调用方已被手册置信度门/留痕管住）。
//	  某个依赖为 nil ⇒ 对应路由不进闭集 ⇒ api 形态方案在归一化期被
//	  fail-fast 拒绝（诚实语义：不宣传一个不可执行的动作）。
type PlaybookDeps struct {
	// ForceRecoverCredential：单凭据强制恢复（admin.Handler.ForceRecoverCredential
	// 的单一真源实现；body: {"credential_id": 123}）。
	ForceRecoverCredential func(ctx context.Context, credID int64) ([]string, error)
	// IndexRefresh：credential_model_index 立即刷新（bg.AutoIndexRefresher；
	// 零参，幂等重算）。
	IndexRefresh interface{ RefreshOnce(ctx context.Context) error }
	// CredibilityScan：模型可信度扫描（bg.ModelCredibilityWorker；
	// body: {"credential_id":1,"raw_model_name":"x"} 单点 或 {"all":true} 全量）。
	CredibilityScan interface {
		ScanNode(ctx context.Context, credentialID int64, rawModelName string) (*modelcredibility.Result, error)
		ScanAll(ctx context.Context) (int, error)
	}
}

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
func initErrorPlaybook(ctx context.Context, sqlDB *sql.DB, deps PlaybookDeps) (
	*errorplaybook.SQLStore, errorplaybook.ActionRegistry, *errorplaybook.Engine, *errorplaybook.Worker,
) {
	if sqlDB == nil {
		return nil, nil, nil, nil
	}

	store := errorplaybook.NewStore(sqlDB)

	// call_admin_api 扩展路由：把装配侧已有的能力登记成闭集路由。
	// ★ 任务 3（2026-11-21）：三条高频处置端点 + 既有 PI 触发。
	//   安全边界（owner 默认档）：三条都是变更类 POST ⇒ PI 自动执行必须过
	//   0.80 置信度门；均不产旧值快照（恢复/刷新/补算都是幂等重算或状态机
	//   自收敛动作，时点回滚会覆盖运行时最新态）⇒ 留痕照记、不可回滚。
	extraRoutes := buildErrorPlaybookExtraRoutes(deps)
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

// buildErrorPlaybookExtraRoutes 组装 call_admin_api 的扩展路由（闭集的一部分）。
//
//	依赖为 nil ⇒ 对应路由不登记 ⇒ 归一化期 fail-fast（不宣传不可执行的动作）。
//	已知闭集（HasRoute / 错误提示口径）：
//	  POST /api/admin/pi-analysis/trigger        —— PI 错误分析批触发（analyzer 接线时）
//	  POST /api/admin/credentials/force-recover  —— 单凭据强制恢复（body: credential_id）
//	  POST /api/admin/auto-route/refresh         —— credential_model_index 立即刷新
//	  POST /api/admin/model-credibility/trigger  —— 可信度单点/全量扫描（body: credential_id+raw_model_name | all=true）
func buildErrorPlaybookExtraRoutes(deps PlaybookDeps) []errorplaybook.AdminRoute {
	routes := []errorplaybook.AdminRoute{}
	if piErrorAnalyzerGlobal != nil {
		analyzer := piErrorAnalyzerGlobal
		routes = append(routes, errorplaybook.AdminRoute{
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
	if deps.ForceRecoverCredential != nil {
		forceRecover := deps.ForceRecoverCredential
		routes = append(routes, errorplaybook.AdminRoute{
			Method: "POST",
			Path:   "/api/admin/credentials/force-recover",
			Handler: func(ctx context.Context, body json.RawMessage) (int, string, error) {
				var req struct {
					CredentialID int64 `json:"credential_id"`
				}
				if len(body) == 0 || json.Unmarshal(body, &req) != nil || req.CredentialID <= 0 {
					return 0, "", errorplaybook.NewPlanInvalid("credentials/force-recover requires body {credential_id: <positive int64>}")
				}
				steps, err := forceRecover(ctx, req.CredentialID)
				if err != nil {
					// 业务性失败（凭据不存在/非 active）⇒ 可安全回显；其余原样上抛（留痕记错误）
					return 0, "", err
				}
				return 200, strings.Join(steps, "; "), nil
			},
		})
	}
	if deps.IndexRefresh != nil {
		refresher := deps.IndexRefresh
		routes = append(routes, errorplaybook.AdminRoute{
			Method: "POST",
			Path:   "/api/admin/auto-route/refresh",
			Handler: func(ctx context.Context, _ json.RawMessage) (int, string, error) {
				if err := refresher.RefreshOnce(ctx); err != nil {
					return 500, "", err
				}
				return 200, "credential_model_index refreshed", nil
			},
		})
	}
	if deps.CredibilityScan != nil {
		scanner := deps.CredibilityScan
		routes = append(routes, errorplaybook.AdminRoute{
			Method: "POST",
			Path:   "/api/admin/model-credibility/trigger",
			Handler: func(ctx context.Context, body json.RawMessage) (int, string, error) {
				var req struct {
					CredentialID int64  `json:"credential_id"`
					RawModelName string `json:"raw_model_name"`
					All          bool   `json:"all"`
				}
				if len(body) == 0 || json.Unmarshal(body, &req) != nil {
					return 0, "", errorplaybook.NewPlanInvalid("model-credibility/trigger requires body {credential_id, raw_model_name} or {all: true}")
				}
				if req.All {
					n, err := scanner.ScanAll(ctx)
					if err != nil {
						return 500, "", err
					}
					return 200, fmt.Sprintf("credibility scan-all done (%d groups)", n), nil
				}
				if req.CredentialID <= 0 || req.RawModelName == "" {
					return 0, "", errorplaybook.NewPlanInvalid("model-credibility/trigger requires credential_id and raw_model_name (or all=true)")
				}
				res, err := scanner.ScanNode(ctx, req.CredentialID, req.RawModelName)
				if err != nil {
					return 500, "", err
				}
				summary := "node scanned"
				if res != nil {
					summary = fmt.Sprintf("node scanned: overall=%.2f verdict=%s status=%s anomalies=%d",
						res.Overall, res.Verdict, res.Status, len(res.Anomalies))
				}
				return 200, summary, nil
			},
		})
	}
	return routes
}
