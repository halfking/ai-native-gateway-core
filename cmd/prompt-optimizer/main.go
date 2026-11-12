// Command prompt-optimizer 提供提示词优化的独立 HTTP 服务。
//
// 设计约束（2026-11-11 落地）：
//   - 本仓使用 vendor/ 模式且不引入新依赖 ⇒ HTTP 层用标准库 net/http，
//     不用 gin/echo（虽已 vendor echo，但本服务无路由组需求，不增依赖面）。
//   - 日志统一走 internal/logging + pkg/logger，与仓内其它 cmd 保持一致。
//   - 配置以 flag 为准、环境变量兜底，避免额外引入 YAML 解析依赖。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	promptcontext "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/context"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/evaluation"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/harness"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies"
	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

func main() {
	var (
		httpAddr       = flag.String("http", ":8090", "HTTP listen address")
		logLevel       = flag.String("log-level", "info", "Log level (debug|info|warn|error)")
		enableHarness  = flag.Bool("harness", true, "Inject harness elements (tools/safety/errors)")
		maxIterations  = flag.Int("max-iterations", 3, "Max optimisation iterations")
		evaluatorModel = flag.String("evaluator-model", envOr("EVALUATOR_MODEL", "gpt-4"), "Model used by the evaluator")
	)
	flag.Parse()

	configureLogging(*logLevel)

	log := logger.New("prompt-optimizer")
	log.Info("starting prompt optimizer service",
		"http_addr", *httpAddr,
		"harness_enabled", *enableHarness,
		"max_iterations", *maxIterations,
		"evaluator_model", *evaluatorModel,
	)

	svc := newService(*enableHarness, *maxIterations, *evaluatorModel)
	srv := &http.Server{
		Addr:              *httpAddr,
		Handler:           svc.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		log.Info("http server listening", "addr", *httpAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown failed", "error", err.Error())
	}
	log.Info("prompt optimizer stopped")
}

// service 持有优化器与评估器，供 HTTP handler 使用。
type service struct {
	opt       optimizer.Optimizer
	evaluator optimizer.Evaluator

	// hookCache 是网关 Hook 契约端点的结果缓存（SHA256 键）。
	hookCache *hookCache

	// memoraWriter 把每次优化产出写回 Memora（best-effort）。
	//
	// 这是"历史会话信息"闭环的那一半：优化 → 落库 → 下一轮优化能检索到
	// 上一轮的产出 ⇒ 优化效果随使用次数累积，而不是每次从零开始。
	// 写失败**不阻断**优化响应（记忆后端不是关键路径）。
	memoraWriter *memora.Client
}

func newService(enableHarness bool, maxIterations int, evaluatorModel string) *service {
	structural := strategies.NewStructural(strategies.DefaultStructuralConfig())
	fewshot := strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil)

	evalCfg := evaluation.DefaultEvaluatorConfig()
	evalCfg.EvaluatorModel = evaluatorModel

	// LLM 通道：把 BaseURL 指向本仓网关即可复用网关路由到内部小模型。
	// 未配置时评估器保持无客户端 —— 需要模型的路径会明确报错，
	// 而不是静默给一个看似正常的分数。
	var llmClient *llm.Client
	if base := os.Getenv("LLM_BASE_URL"); base != "" {
		client, err := llm.New(llm.Config{
			BaseURL: base,
			APIKey:  os.Getenv("LLM_API_KEY"),
			Model:   evaluatorModel,
		})
		if err != nil {
			slog.Warn("llm client init failed, model-dependent paths disabled", "error", err.Error())
		} else {
			llmClient = client
			slog.Info("llm channel enabled", "base_url", base, "model", evaluatorModel)
		}
	} else {
		slog.Info("llm channel disabled (set LLM_BASE_URL to enable output evaluation)")
	}

	evaluator := evaluation.NewEvaluator(evalCfg, evaluation.DefaultMetrics(), llmClient)

	opts := []optimizer.OptimizerOption{
		optimizer.WithStrategies(structural, fewshot),
		optimizer.WithEvaluator(evaluator),
	}

	// 记忆后端（memora）：未配置 LLM_GATEWAY_MEMORA_BASE_URL 时不接线，
	// context.Manager 退化为"无历史"模式，优化器仍可用（只是少了历史增强）。
	//
	// 环境变量沿用网关既有约定（见 cmd/gateway/memory_adapter.go）：
	//   LLM_GATEWAY_MEMORA_BASE_URL / _API_KEY / _PROJECT_ID
	if memoraBase := os.Getenv("LLM_GATEWAY_MEMORA_BASE_URL"); memoraBase != "" {
		memoraClient := memclient.NewClient(memclient.ClientConfig{
			BaseURL:   memoraBase,
			APIKey:    os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
			ProjectID: os.Getenv("LLM_GATEWAY_MEMORA_PROJECT_ID"),
		})
		ctxCfg := promptcontext.DefaultConfig()
		ctxCfg.UserID = envOr("MEMORA_USER_ID", "prompt-optimizer")
		opts = append(opts, optimizer.WithContextManager(
			promptcontext.NewManager(memoraClient, ctxCfg),
		))
	}

	if enableHarness {
		// 生产用 ProductionInjectorConfig：注入安全约束与错误处理，
		// **不注入工具章节** —— 本仓当前唯一的工具注册表是占位用的
		// MockToolRegistry，把它写进真实请求会告诉模型"你有这些工具"，
		// 而上游并不存在（详见 harness.ProductionInjectorConfig 注释）。
		opts = append(opts, optimizer.WithHarnessInjector(
			harness.NewInjector(
				harness.ProductionInjectorConfig(),
				harness.NewMockToolRegistry(),
				harness.NewMockSafetyRules(),
			),
		))
	}

	// 真实 Memora（v2 fail-closed 组）优先；未配置时回落到网关既有的
	// MemOS 兼容客户端（历史行为不变）。
	memoraClient := newMemoraV2Client()
	if memoraClient != nil {
		ctxCfg := promptcontext.DefaultConfig()
		ctxCfg.Enabled = true
		ctxCfg.UserID = memoraClient.ActorID()
		opts = append(opts, optimizer.WithContextManager(
			promptcontext.NewManager(memoraClient, ctxCfg),
		))
		slog.Info("memora v2 connected", "project_id", memoraClient.ProjectID(), "actor_id", memoraClient.ActorID())
	}

	ttl := time.Duration(envOrInt("PROMPT_OPTIMIZER_CACHE_TTL_SECONDS", 3600)) * time.Second

	return &service{
		opt:          optimizer.NewOptimizer(opts...),
		evaluator:    evaluator,
		hookCache:    newHookCache(ttl),
		memoraWriter: memoraClient,
	}
}

// recordOptimization 把一次优化产出 best-effort 写回 Memora。
//
// 写进去的是"这次优化做了什么"（原提示词摘要 + 优化后提示词 + 策略），
// 下一轮 context.Manager 检索历史时就能取到它 ⇒ 形成闭环。
func (s *service) recordOptimization(ctx context.Context, tenantID, model, original, optimized string, strategies []string) {
	if s.memoraWriter == nil {
		return
	}
	// ★ 写回必须脱离请求的生命周期：HTTP 响应一返回，r.Context() 立刻被取消，
	//   挂在它上面的写回请求会收到 "context canceled"（实测：E2E 脚本第 7 条
	//   断言因此失败）。context.WithoutCancel 保留 trace 等 value，
	//   只丢掉取消与超时，再自己加一个独立超时。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	preview := fmt.Sprintf("[提示词优化] 原提示词：%s ｜ 优化后：%s ｜ 策略：%s",
		truncateText(original, 300), truncateText(optimized, 800), strings.Join(strategies, ","))
	_, err := s.memoraWriter.Ingest(ctx, "gateway", []memora.IngestItem{{
		SourceType:     "session_summary",
		SourceID:       fmt.Sprintf("po-opt-%d", time.Now().UTC().UnixNano()),
		ContentRef:     "prompt-optimizer/optimizations",
		ContentPreview: preview,
		Tags:           []string{"prompt-optimizer", "optimization"},
		Metadata:       map[string]any{"tenant_id": tenantID, "model": model, "strategies": strategies},
		RunID:          "prompt-optimizer",
	}})
	if err != nil {
		slog.Warn("memora write-back failed", "error", err.Error())
	}
}

// truncateText 截断文本（按 rune，避免切坏 UTF-8）。
func truncateText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// newMemoraV2Client 构造真实 Memora v2 客户端。
//
// 环境变量：
//
//	PROMPT_OPTIMIZER_MEMORA_BASE_URL     服务地址（默认 http://127.0.0.1:12113）
//	PROMPT_OPTIMIZER_MEMORA_JWT_SECRET   HS256 密钥（或用 MEMORA_JWT_SECRET）
//	PROMPT_OPTIMIZER_MEMORA_PROJECT_ID   项目作用域
//	PROMPT_OPTIMIZER_MEMORA_ACTOR_ID     授权主体（默认 local-ops）
//	PROMPT_OPTIMIZER_MEMORA_TENANT_ID    租户（默认 default）
//
// 缺任何一项都返回 nil（服务继续以"无历史"模式运行），并记录原因 ——
// 不因为记忆后端没接上就让整个优化服务起不来。
func newMemoraV2Client() *memora.Client {
	base := envOr("PROMPT_OPTIMIZER_MEMORA_BASE_URL", envOr("MEMORA_BASE_URL", ""))
	if base == "" {
		return nil
	}
	secret := envOr("PROMPT_OPTIMIZER_MEMORA_JWT_SECRET", os.Getenv("MEMORA_JWT_SECRET"))
	if secret == "" {
		slog.Warn("memora v2 not connected: jwt secret missing (PROMPT_OPTIMIZER_MEMORA_JWT_SECRET)")
		return nil
	}
	client, err := memora.New(memora.Config{
		BaseURL:   base,
		JWTSecret: secret,
		Issuer:    envOr("MEMORA_V2_JWT_ISSUER", memora.DefaultIssuer),
		Audience:  envOr("MEMORA_V2_JWT_AUDIENCE", memora.DefaultAudience),
		TenantID:  envOr("MEMORA_TENANT_ID", memora.DefaultTenantID),
		ActorID:   envOr("MEMORA_ACTOR_ID", "local-ops"),
		ProjectID: envOr("PROMPT_OPTIMIZER_MEMORA_PROJECT_ID", "prompt-optimizer"),
	})
	if err != nil {
		slog.Warn("memora v2 not connected", "error", err.Error())
		return nil
	}
	return client
}

// routes 注册 HTTP 路由。
//
// ★ 部署阻断项（2026-11-12 批判审计 P2）：本 mux **零鉴权**——所有端点对
// 能触达端口的调用方完全开放，而服务持有 LLM_API_KEY 可调上游 ⇒ 一旦发布
// 到有外网面的机器＝「开放 LLM 代理」（prompt 外泄 + 额度盗刷）。接线/
// 公网部署前必须补鉴权中间件（bearer token 或仅 loopback+反代鉴权）；
// docker-compose 已把端口发布收敛到 127.0.0.1 止血。
func (s *service) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "healthy",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	})

	// 网关内置 Hook 的契约端点（domains/hooks/promptoptimization）。
	// 缺了它，Hook 接线后只会一直 404 回退。
	mux.HandleFunc("/api/v1/prompts/optimize", s.handleHookOptimize)

	mux.HandleFunc("/api/v1/optimize", s.handleOptimize)
	mux.HandleFunc("/api/v1/optimize/batch", s.handleOptimizeBatch)
	mux.HandleFunc("/api/v1/optimize/evaluate", s.handleOptimizeWithEvaluation)
	mux.HandleFunc("/api/v1/compare", s.handleCompare)

	return mux
}

// OptimizeRequest 优化请求。
type OptimizeRequest struct {
	Prompt        string `json:"prompt"`
	TaskType      string `json:"task_type"`
	Domain        string `json:"domain"`
	TargetModel   string `json:"target_model"`
	UseMemora     bool   `json:"use_memora"`
	InjectHarness bool   `json:"inject_harness"`
	MaxIterations int    `json:"max_iterations"`
}

// BatchOptimizeRequest 批量优化请求。
type BatchOptimizeRequest struct {
	Prompts       []string `json:"prompts"`
	TaskType      string   `json:"task_type"`
	Domain        string   `json:"domain"`
	InjectHarness bool     `json:"inject_harness"`
}

// EvaluateRequest 带评估的优化请求。
type EvaluateRequest struct {
	Prompt     string               `json:"prompt"`
	TestCases  []optimizer.TestCase `json:"test_cases"`
	Metrics    []string             `json:"metrics"`
	ABTest     bool                 `json:"ab_test"`
	SampleSize int                  `json:"sample_size"`
}

// CompareRequest 对比请求。
type CompareRequest struct {
	PromptA string `json:"prompt_a"`
	PromptB string `json:"prompt_b"`

	// Mode 对比口径：quality（默认，结构质量，无需模型通道）
	// 或 output（跑模型比输出，需要 LLM 通道）。
	Mode string `json:"mode"`

	// TestCases 仅 mode=output 时需要。
	TestCases []optimizer.TestCase `json:"test_cases"`
}

// errorResponse 统一错误响应。
type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to encode response", "error", err.Error())
	}
}

// decodeJSON 解析请求体。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body: " + err.Error()})
		return false
	}
	return true
}

// handleOptimize 处理单条优化请求。
func (s *service) handleOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req OptimizeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "prompt must not be empty"})
		return
	}

	result, err := s.opt.Optimize(r.Context(), req.Prompt, optimizer.OptimizeOptions{
		TaskType:      req.TaskType,
		Domain:        req.Domain,
		TargetModel:   req.TargetModel,
		UseMemora:     req.UseMemora,
		InjectHarness: req.InjectHarness,
		MaxIterations: req.MaxIterations,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	// 历史闭环：把本次优化写回 Memora（best-effort，不影响响应）。
	go s.recordOptimization(r.Context(), "", req.TargetModel, req.Prompt, result.Optimized, result.AppliedStrategies)

	writeJSON(w, http.StatusOK, result)
}

// handleOptimizeBatch 处理批量优化请求。
func (s *service) handleOptimizeBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req BatchOptimizeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Prompts) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "prompts must not be empty"})
		return
	}

	results, err := s.opt.OptimizeBatch(r.Context(), req.Prompts, optimizer.OptimizeOptions{
		TaskType:      req.TaskType,
		Domain:        req.Domain,
		InjectHarness: req.InjectHarness,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleOptimizeWithEvaluation 处理带评估的优化请求。
func (s *service) handleOptimizeWithEvaluation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req EvaluateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "prompt must not be empty"})
		return
	}
	if len(req.TestCases) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "test_cases must not be empty"})
		return
	}

	result, err := s.opt.OptimizeWithEvaluation(r.Context(), req.Prompt, optimizer.EvaluationOptions{
		TestCases:  req.TestCases,
		Metrics:    req.Metrics,
		ABTest:     req.ABTest,
		SampleSize: req.SampleSize,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleCompare 对比两个提示词。
//
// 两种口径（由请求的 mode 决定）：
//   - mode=quality（默认）：**结构质量**对比，不需要模型通道，任何环境都能用；
//     衡量的是"该有的要素齐不齐"，**不能**证明效果更好。
//   - mode=output：跑模型后对比输出相似度，需要配置 LLM 通道；
//     衡量的是"输出像不像期望答案"，仍不等于"答案对不对"。
//
// 口径必须在响应里回显（mode 字段），否则消费方会误以为拿到了效果结论。
func (s *service) handleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req CompareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.PromptA == "" || req.PromptB == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "prompt_a and prompt_b must not be empty"})
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = "quality"
	}

	switch mode {
	case "quality":
		scoreA, err := s.evaluator.EvaluatePromptQuality(r.Context(), req.PromptA)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		scoreB, err := s.evaluator.EvaluatePromptQuality(r.Context(), req.PromptB)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":           "quality",
			"metric":         "prompt_quality",
			"prompt_a":       req.PromptA,
			"prompt_b":       req.PromptB,
			"score_a":        scoreA,
			"score_b":        scoreB,
			"difference":     scoreB - scoreA,
			"winner":         winnerOf(scoreA, scoreB),
			"interpretation": "结构质量分：衡量提示词要素完备度，不等于实际效果差异",
		})

	case "output":
		if len(req.TestCases) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "test_cases must not be empty in output mode"})
			return
		}
		evalOpts := optimizer.EvaluationOptions{TestCases: req.TestCases}
		scoreA, err := s.evaluator.Evaluate(r.Context(), req.PromptA, evalOpts)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{
				Error: "evaluate prompt_a: " + err.Error() +
					"（output 模式需要 LLM 通道：请配置 LLM_BASE_URL/LLM_API_KEY；" +
					"或改用 mode=quality 做结构质量对比）",
			})
			return
		}
		scoreB, err := s.evaluator.Evaluate(r.Context(), req.PromptB, evalOpts)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{
				Error: "evaluate prompt_b: " + err.Error() +
					"（output 模式需要 LLM 通道：请配置 LLM_BASE_URL/LLM_API_KEY）",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":           "output",
			"metric":         "output_similarity",
			"prompt_a":       req.PromptA,
			"prompt_b":       req.PromptB,
			"score_a":        scoreA,
			"score_b":        scoreB,
			"difference":     scoreB - scoreA,
			"winner":         winnerOf(scoreA, scoreB),
			"interpretation": "输出相似度分：衡量输出像不像期望答案，不等于答案正确性",
		})

	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "mode must be one of: quality, output",
		})
	}
}

// winnerOf 判定胜者（差值小于 0.01 视为平局）。
func winnerOf(scoreA, scoreB float64) string {
	switch {
	case scoreB-scoreA > 0.01:
		return "B"
	case scoreA-scoreB > 0.01:
		return "A"
	default:
		return "tie"
	}
}

// configureLogging 按级别配置全局 slog。
//
// 与 cmd/license-authority 保持同一口径：logging.NewServiceLogger 内部已带
// 「构造失败降级到裸 JSONHandler」的兜底，绝不让服务因日志初始化失败起不来。
func configureLogging(level string) {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	slog.SetDefault(logging.NewServiceLogger(lvl))
}

// envOr 读取环境变量，缺省时返回 fallback。
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrInt 读取整型环境变量，缺省或非法时返回 fallback。
func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
