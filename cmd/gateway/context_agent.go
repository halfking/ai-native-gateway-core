// Package main —— context_agent.go
//
// 装配「网关侧上下文智能体」（domains/contextagent）与它的两个出口：
//
//	① HTTP 门面：/api/system/context-agent/{resolve,tool,record/{id}}
//	   —— 客户端驱动形态（自己提交装配后的请求）与网关驱动形态
//	      （网关提交 + 记忆工具回环 + 返回最终答案）都走这里。
//	② MCP 本地工具：把「按步取回明细」「语义召回长期记忆」注册成
//	   MCP 数据面可列出、可调用的本地工具。
//
// ★ 开关（默认关）：
//
//	LLM_GATEWAY_CONTEXT_AGENT_ENABLED=1   总开关（HTTP 门面 + MCP 本地工具）
//	LLM_GATEWAY_CONTEXT_AGENT_RECORD_DIR  可还原记录落盘目录（不配 ⇒ 不记录）
//	LLM_GATEWAY_CONTEXT_AGENT_LOOPBACK_URL 回灌端点（不配 ⇒ 由监听地址推导）
//
// ★ 为什么默认关：这是**新增能力面**（新的端点 + 新的 MCP 工具），
//
//	默认开启等于改变现网行为面。按仓内既有纪律（memora、mcp 的 bootstrap
//	都是默认关），新能力一律默认关、由环境变量显式打开。
//
// ★ 依赖是**软依赖**：没有 PG 就没有 L0/L1 层，没有 Memora 就没有 L2 层，
//
//	缺哪层就少哪层，**不阻断**其它层，也不阻断请求本身
//	（记忆是增强，不是前置条件）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/contextagent"
	"github.com/kaixuan/llm-gateway-go/domains/memory"
	"github.com/kaixuan/llm-gateway-go/mcp"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

// 环境变量名。
const (
	envContextAgentEnabled   = "LLM_GATEWAY_CONTEXT_AGENT_ENABLED"
	envContextAgentRecordDir = "LLM_GATEWAY_CONTEXT_AGENT_RECORD_DIR"
	envContextAgentLoopback  = "LLM_GATEWAY_CONTEXT_AGENT_LOOPBACK_URL"
	// envContextAgentModelKey 是**模型面凭据**（sk-* 明文，不含 "Bearer "）。
	//
	// ★ 为什么必须有：/resolve 挂在管理令牌中间件后面 ⇒ 请求里的
	// Authorization 恒为管理令牌（ops_ 形态），而数据面只认 sk-*
	// ⇒ 用它回灌必然 401；且它的哈希与写记忆用的 sk-* 哈希不同
	// ⇒ 记忆命名空间也永远对不上。
	envContextAgentModelKey = "LLM_GATEWAY_CONTEXT_AGENT_MODEL_KEY"
	// envContextAgentAllowRequestCredential 是否允许调用方用请求头
	// X-Model-Key 覆盖模型面凭据。
	//
	// ★ 默认关：凭据决定记忆命名空间 ⇒ 让客户端自选 = 放大同租户内的
	//	记忆读取面（上一轮已拍板）。
	envContextAgentAllowRequestCredential = "LLM_GATEWAY_CONTEXT_AGENT_ALLOW_REQUEST_CREDENTIAL"
)

// contextAgentBundle 是装配产物。
type contextAgentBundle struct {
	// Agent 是上下文智能体本体。
	Agent *contextagent.Agent
	// Service 是 HTTP 门面的服务层。
	Service *contextagent.Service
	// LocalTools 是 MCP 本地工具注册表（交给 registerMCPGateway 注入）。
	LocalTools *mcp.LocalToolRegistry
}

// contextAgentDeps 是装配所需的外部依赖。
type contextAgentDeps struct {
	// Pool 是网关自己的 PG 池（nil ⇒ 无 L0/L1 层）。
	Pool *pgxpool.Pool
	// Memory 是 Memora 读端（nil 或已禁用 ⇒ 无 L2 层）。
	Memory memory.Reader
	// Listen 是网关监听地址（推导回灌端点用）。
	Listen string
	// AdminAPIKey 是管理令牌（保护 HTTP 门面）。
	AdminAPIKey string
}

// registerContextAgent 装配上下文智能体并挂载它的 HTTP 门面。
//
// ★ 失败只记日志、不阻断启动：这是附加能力，不能因为它把模型网关拖下线。
//
// ★ 返回 nil 表示「没开」或「装配失败」，两种情况都不挂路由
//
//	（路径落 404，语义与「未部署该子系统」一致）。
func registerContextAgent(mux *http.ServeMux, logger *slog.Logger, deps contextAgentDeps) *contextAgentBundle {
	if logger == nil {
		logger = slog.Default()
	}
	if !envBoolStrict(envContextAgentEnabled) {
		logger.Info("context agent: disabled (set " + envContextAgentEnabled + "=1 to enable)")
		return nil
	}

	bundle := &contextAgentBundle{LocalTools: mcp.NewLocalToolRegistry()}

	agent := &contextagent.Agent{
		InjectTools:        true,
		MaxRounds:          contextagent.DefaultMaxRounds,
		MaxToolResultBytes: contextagent.DefaultToolResultBytes,
	}

	// ── L0 索引层 + L1 明细层：网关自己的 PG ──
	if deps.Pool != nil {
		db := contextagent.PGQueryer{Pool: deps.Pool}
		agent.IndexLoader = contextagent.NewPGIndexLoader(db)
		agent.StepLoader = contextagent.NewPGStepDetailLoader(db)
	} else {
		logger.Warn("context agent: no PG pool, L0/L1 memory layers disabled")
	}

	// ── L2 长期记忆层：Memora（语义召回，非按步寻址）──
	if deps.Memory != nil && !deps.Memory.Disabled() {
		agent.LongTerm = contextagent.NewMemoraLongTermLoader(deps.Memory)
	} else {
		logger.Info("context agent: memora unavailable, L2 long-term layer disabled")
	}

	// ── 可还原记录（可选）──
	if dir := strings.TrimSpace(os.Getenv(envContextAgentRecordDir)); dir != "" {
		rec, err := contextagent.NewFileRecorder(dir)
		if err != nil {
			logger.Error("context agent: recorder init failed, recording disabled", "error", err)
		} else {
			agent.Recorder = rec
		}
	}

	// ── 回灌提交器：装配后提交上游，走网关自己的在役链路 ──
	endpoint := strings.TrimSpace(os.Getenv(envContextAgentLoopback))
	if endpoint == "" {
		endpoint = loopbackChatURL(deps.Listen)
	}
	agent.Submitter = &contextagent.LoopbackSubmitter{
		Endpoint: endpoint,
		Timeout:  120 * time.Second,
	}

	bundle.Agent = agent
	bundle.Service = &contextagent.Service{
		Agent: agent,
		// ★ 模型面凭据：回灌上游 + 记忆命名空间都要用（见 envContextAgentModelKey）。
		ModelCredential: strings.TrimSpace(os.Getenv(envContextAgentModelKey)),
		// ★ 默认**不允许**调用方自选凭据（读面放大风险）。
		AllowRequestCredential: envBoolStrict(envContextAgentAllowRequestCredential),
	}

	// ── MCP 本地工具：把两个记忆能力暴露给 MCP 数据面 ──
	registerContextMemoryTools(bundle.LocalTools, agent)

	// ── 挂载 HTTP 门面（管理令牌保护）──
	handler := middleware.NewAdminTokenMiddleware(deps.AdminAPIKey).Wrap(bundle.Service.Handler())
	mux.Handle("/api/system/context-agent", handler)
	mux.Handle("/api/system/context-agent/", handler)

	logger.Info("context agent mounted",
		"http_face", "/api/system/context-agent",
		"endpoints", "POST /resolve, POST /tool, GET /record/{request_id}",
		"mcp_local_tools", strings.Join(contextagent.ToolNames, ","),
		"index_layer", agent.IndexLoader != nil,
		"step_layer", agent.StepLoader != nil,
		"longterm_layer", agent.LongTerm != nil,
		"recorder", agent.Recorder != nil,
		"loopback_endpoint", endpoint,
	)
	return bundle
}

// registerContextMemoryTools 把两个记忆能力注册成 MCP 本地工具。
//
// ★ 治理不被绕过：本地工具照样过 MCP 的授权链（scope → 可见性 →
//
//	key×tool 允许表（deny-wins）→ 审批 → 参数白名单/JSON Schema），
//	见 mcp/local_tools.go 的 handleLocalToolCall。
//
// ★ 租户边界：会话 id 由调用方给出，但**只能读到本租户**的会话
//
//	（PG 查询恒带 tenant_id 条件）⇒ 越权读不到别的租户。
func registerContextMemoryTools(reg *mcp.LocalToolRegistry, agent *contextagent.Agent) {
	if reg == nil || agent == nil {
		return
	}

	reg.Register(mcp.LocalTool{
		Name: contextagent.ToolLoadStepDetail,
		Description: "按步骤号取回该步骤的完整历史明细（工具输出、报错、文件内容）。" +
			"当你需要某个早期步骤的确切细节（路径、错误原文、命令输出）时调用。",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "step_index": {"type": "integer", "description": "步骤号", "minimum": 1},
    "session_id": {"type": "string", "description": "会话 id（省略则用本次请求的会话）"}
  },
  "required": ["step_index"],
  "additionalProperties": false
}`),
		AllowedParams: []string{"step_index", "session_id"},
		Visibility:    mcp.VisibilityPublic,
		Class:         mcp.ClassRead,
		Group:         "memory",
		Handler: func(ctx context.Context, id *mcp.Identity, args map[string]any) (any, error) {
			cid := contextIdentityFromMCP(id, args)
			res := contextagent.ServeMemoryTool(ctx, contextagent.ToolDeps{
				Identity:   cid,
				StepLoader: agent.StepLoader,
				LongTerm:   agent.LongTerm,
				MaxBytes:   agent.MaxToolResultBytes,
			}, contextagent.ToolCall{
				ID:        "mcp-" + contextagent.ToolLoadStepDetail,
				Name:      contextagent.ToolLoadStepDetail,
				Arguments: mustJSON(args),
			})
			if res.Error != "" {
				// ★ 工具失败也给模型一句可读说明，而不是把整个请求打断。
				return res.Content, nil
			}
			return res.Content, nil
		},
	})

	reg.Register(mcp.LocalTool{
		Name: contextagent.ToolSearchMemory,
		Description: "按语义检索长期记忆（跨会话的既有事实、偏好、踩过的坑）。" +
			"当当前任务的答案可能依赖历史经验时调用；结果不保证与当前任务相关。",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "检索用的自然语言查询"},
    "top_k": {"type": "integer", "description": "返回条数上限", "minimum": 1, "maximum": 20},
    "session_id": {"type": "string", "description": "会话 id（用于圈定记忆命名空间）"}
  },
  "required": ["query"],
  "additionalProperties": false
}`),
		AllowedParams: []string{"query", "top_k", "session_id"},
		Visibility:    mcp.VisibilityPublic,
		Class:         mcp.ClassRead,
		Group:         "memory",
		Handler: func(ctx context.Context, id *mcp.Identity, args map[string]any) (any, error) {
			cid := contextIdentityFromMCP(id, args)
			res := contextagent.ServeMemoryTool(ctx, contextagent.ToolDeps{
				Identity:   cid,
				StepLoader: agent.StepLoader,
				LongTerm:   agent.LongTerm,
				MaxBytes:   agent.MaxToolResultBytes,
			}, contextagent.ToolCall{
				ID:        "mcp-" + contextagent.ToolSearchMemory,
				Name:      contextagent.ToolSearchMemory,
				Arguments: mustJSON(args),
			})
			return res.Content, nil
		},
	})
}

// contextIdentityFromMCP 把 MCP 身份 + 工具参数翻译成上下文智能体的身份。
//
// ★ 租户来自**凭证**（id.TenantID），不来自参数 ⇒ 调用方无法通过参数
//
//	把租户改掉。会话来自参数（MCP 客户端自己知道它在哪个会话里），
//	但读的时候仍受 tenant_id 条件约束。
//
// ★ 参数白名单纪律（审计轮修复）：
//
//	旧实现读 args["task"] 与 args["project"]，但两个本地工具的
//	AllowedParams 里**都没有**这两个参数，而 mcp/policy.go 的
//	validateParams 对未列入白名单的参数是**直接拒绝**（ErrForbidden）
//	⇒ 这两段读取恒为死代码（handler 根本走不到）。
//	⇒ 现在**删掉**这两段读取，而不是把它们加进白名单：
//		· project 不参与记忆寻址（项目是归属维度，不是寻址维度）；
//		· task 会决定命名空间 ⇒ 让客户端自选 task = 扩大同租户内的
//		  记忆读取面（上一轮已拍板：不让客户端自选作用域）。
//	⇒ 作用域只由**凭证**（tenant + key）与 **session_id 参数**决定。
//
// ★ 记忆命名空间：用 **MCP 凭证自己的**稳定 id 派生（写侧同口径）。
//
//	★ 诚实边界（**结构性**，不是可修的缺陷）：
//	MCP 凭证（mcp.Key / HashSecret）与数据面 sk-* 凭据是**两套凭证体系**
//	⇒ 即便哈希口径完全一致，两边的 api_key_id 也必然不同
//	⇒ MCP 侧**召不到**数据面写下的记忆。
//	⇒ 该边界已登记在设计档「记忆命名空间的边界」一节；
//		本函数把边界**显式带进**命名空间备注，让工具的返回文本能说清楚
//		「为什么召回为空」，而不是让读数看起来像「记忆里没有」。
func contextIdentityFromMCP(id *mcp.Identity, args map[string]any) contextagent.Identity {
	cid := contextagent.Identity{}
	if id != nil {
		cid.TenantID = id.TenantID
		cid.AppCode = id.UserID
	}
	if v, ok := args["session_id"].(string); ok {
		cid.Session = strings.TrimSpace(v)
	}
	// 命名空间：凭证分量用 MCP 凭证 id 的稳定哈希，任务分量用会话。
	if id != nil && id.KeyID != "" && cid.Session != "" {
		apiKeyID := memory.StableHashKey("mcp:" + id.KeyID)
		taskID := "s:" + cid.Session
		cid.MemoryAPIKeyID = apiKeyID
		cid.MemoryTaskID = taskID
		cid.MemoryNamespace = memory.UserID(cid.TenantID, apiKeyID, taskID)
		cid.MemoryNamespaceNote = "命名空间由 MCP 凭证派生；MCP 凭证与数据面 sk-* 凭据是两套体系，命名空间不重叠 ⇒ 这里召不到数据面写入的记忆（已登记的结构性边界）。"
	}
	return cid
}

// mustJSON 把参数序列化成 JSON（失败时返回空对象字面量）。
func mustJSON(args map[string]any) []byte {
	if len(args) == 0 {
		return []byte(`{}`)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

// loopbackChatURL 从监听地址推导网关自己的 chat 端点。
//
// ★ 用途：装配后的请求要**回灌**到网关自己的在役链路（鉴权 → 路由 →
//
//	凭据选择 → 上游 → 计费），而不是在本进程内复刻一遍路由。
func loopbackChatURL(listen string) string {
	port := "8781"
	if _, p, err := net.SplitHostPort(listen); err == nil && p != "" {
		port = p
	} else if strings.HasPrefix(listen, ":") {
		port = strings.TrimPrefix(listen, ":")
	} else if n, err := strconv.Atoi(listen); err == nil && n > 0 {
		port = strconv.Itoa(n)
	}
	return "http://127.0.0.1:" + port + "/v1/chat/completions"
}

// envBoolStrict 解析布尔环境变量（只有 1/true/yes/on 算真）。
//
// ★ 刻意比仓内既有的宽松解析更严：这是**新能力的开关**，
//
//	宁可「配了但没开」也不要「没配却开了」。
func envBoolStrict(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// contextAgentEnabled 报告上下文智能体是否启用（供启动日志与能力清单用）。
func contextAgentEnabled() bool {
	return envBoolStrict(envContextAgentEnabled)
}

// formatContextAgentStatus 给启动日志用的状态串。
func formatContextAgentStatus() string {
	if !contextAgentEnabled() {
		return "disabled"
	}
	return fmt.Sprintf("enabled(record_dir=%q)", strings.TrimSpace(os.Getenv(envContextAgentRecordDir)))
}
