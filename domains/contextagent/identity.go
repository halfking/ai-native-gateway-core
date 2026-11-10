// Package contextagent —— 网关侧上下文智能体（Gateway Context Agent）。
//
// ★ 本包是什么：
// 一个「在网关内部完成上下文识别、记忆分层加载、动态压缩、以及记忆工具的
// 服务与同步回环」的智能体。它把用户描述的流程落成可执行、可测试的代码：
//
//	客户端请求
//	  → [1] 识别（项目 / 任务 / 会话 / 步骤 / 租户 / 协议 / 客户端类型）
//	  → [2] 计划（按预算产出分层计划：索引层 / 步骤明细层 / 长期记忆层 / 尾部）
//	  → [3] 加载（L1 走网关 PG 精确寻址；L2 走 Memora 语义召回）
//	  → [4] 装配（system → L0 索引 → 已加载层 → 最近尾部）
//	  → [5] 提交上游 LLM
//	  → [6] 识别记忆工具调用（load_step_detail / search_memory）
//	  → [7] 网关自己执行该工具（L1/L2 取数）
//	  → [8] 把工具结果并回请求体，同步重新提交（最多 N 轮）
//	  → [9] 记录（原客户端请求字节 + 原上游响应字节 + 中间态 + 各层来源）
//	     ⇒ 事后可**原样还原**客户端请求与 LLM 返回
//
// ★ 为什么分层要这样切（证据见 docs/03-design/02-feature-design/modules/context-agent.md）：
//
//	L0 索引层   每步一行「目标 → 结果」      权威源：public.session_request_summaries
//	L1 步骤层   每一步的完整正文              权威源：public.session_bodies（按 turn_no）
//	L2 长期层   跨会话的长期事实/偏好          权威源：Memora（向量，**语义**寻址）
//
//	Memora 只能做 L2：它的检索请求体只有 {user_id, query, top_k}，
//	**没有任何 id/step 寻址能力**（domains/memory/client/client.go），
//	所以「加载第 N 步的细节」必须由网关自己的 PG 承担。
//
// ★ 依赖方向（避免 import cycle）：
// 本包**不**依赖 domains/streaming。上游提交由 Submitter 接口注入，
// 由调用方（streaming 层）提供实现。
package contextagent

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
	"github.com/kaixuan/llm-gateway-go/internal/sessionid"
)

// Identity 是网关在**请求时**能拿到的身份画像。
//
// ★ 口径纪律：每个字段都必须有真实来源，不做猜测填充。
//
//	Project  —— X-Gw-Project-Id 优先；否则回落 app:<application_code>
//	            （这是仓内唯一在役的「项目」口径，见
//	             sql/migrations/startup/762_session_project_backfill_chain.sql）
//	Task     —— X-Gw-Task-Id（自由文本关联串，仓内无登记表）
//	Session  —— internal/sessionid.Resolve（body 优先，再 header）
//	Step     —— X-Gw-Step-Index 优先；否则由调用方按轮次填充
//	            （网关的原生粒度是「轮」= 一次 HTTP 请求，**不是**「步」）
type Identity struct {
	TenantID   string `json:"tenant_id"`
	APIKeyID   int    `json:"api_key_id"`
	AppCode    string `json:"app_code"`
	Project    string `json:"project"`
	Task       string `json:"task"`
	Session    string `json:"session"`
	StepIndex  int    `json:"step_index"`
	StepID     string `json:"step_id"`
	Protocol   string `json:"protocol"`
	ClientType string `json:"client_type"`
	RequestID  string `json:"request_id"`
}

// IdentityFacts 是识别阶段需要的、**由网关其它部件已经解析好**的事实。
//
// ★ 为什么不自己从 header 重新解析 tenant/api_key：
//
//	租户与 api key 的权威解析在鉴权层（domains/authentication），
//	网关内已经解析过一次；这里复用同一个结论，避免两套口径漂移。
type IdentityFacts struct {
	TenantID string
	APIKeyID int
	AppCode  string
	// ApplicationCode 是鉴权层解析出的 application code（用于 app:<code> 回落）。
	ApplicationCode string
	RequestID       string
	// ClientProtocol 由调用方按请求路径推断（openai-chat / anthropic-messages …）。
	ClientProtocol string
}

// 头名常量。
const (
	HeaderProjectID   = "X-Gw-Project-Id"
	HeaderTaskID      = "X-Gw-Task-Id"
	HeaderStepIndex   = "X-Gw-Step-Index"
	HeaderStepID      = "X-Gw-Step-Id"
	HeaderClientType  = "X-Client-Profile"
	HeaderAgentName   = "X-Agent-Name"
	headerRequestID   = "X-Request-Id"
	headerDeviceSeed  = "X-Device-Seed"
	headerMachineID   = "X-Machine-Id"
	appProjectPrefix  = "app:"
	maxIdentityLength = 128
)

// Recognize 从请求里识别身份。
//
// ★ 会话解析**复用 internal/sessionid.Resolve**（body 优先、再 header），
//
//	不另立一套口径——该包是仓内会话 id 的唯一真源（有棘轮门钉桩）。
//
// ★ 项目回落口径：`app:<application_code>`。理由见 Identity 字段注释；
//
//	注意 `X-Gw-Project-Id` 虽然端到端已接线，但实测**几乎没有任何客户端发送**
//	（docs/全面审计v3/2026-10-01/153-R89BO-…md），所以回落路径才是常态路径。
func Recognize(r *http.Request, body []byte, facts IdentityFacts) Identity {
	id := Identity{
		TenantID:   facts.TenantID,
		APIKeyID:   facts.APIKeyID,
		AppCode:    facts.AppCode,
		Protocol:   facts.ClientProtocol,
		RequestID:  facts.RequestID,
		ClientType: clienttype.Unknown,
	}
	if id.RequestID == "" && r != nil {
		id.RequestID = strings.TrimSpace(r.Header.Get(headerRequestID))
	}

	if r != nil {
		id.Project = sanitizeIdentity(r.Header.Get(HeaderProjectID))
		id.Task = sanitizeIdentity(r.Header.Get(HeaderTaskID))
		id.StepID = sanitizeIdentity(r.Header.Get(HeaderStepID))
		if v := strings.TrimSpace(r.Header.Get(HeaderStepIndex)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				id.StepIndex = n
			}
		}
		if ct := clienttype.Normalize(r.Header.Get(HeaderClientType)); ct != "" {
			id.ClientType = ct
		} else if ct := clienttype.Normalize(r.Header.Get(HeaderAgentName)); ct != "" {
			id.ClientType = ct
		}
	}

	// 项目回落：app:<application_code>。
	if id.Project == "" && facts.ApplicationCode != "" {
		id.Project = appProjectPrefix + facts.ApplicationCode
	}

	// 会话：body 优先，再 header（internal/sessionid 的既定优先级）。
	if sid, _ := sessionid.ResolveRequest(r, body); sid != "" {
		id.Session = sid
	}
	if id.Session == "" && r != nil {
		if sid, _ := sessionid.FromHeaders(r.Header); sid != "" {
			id.Session = sid
		}
	}

	return id
}

// sanitizeIdentity 清洗一个身份字段：去空白、限长、拒绝控制字符。
//
// ★ 不做「兜底值」填充：身份字段宁可为空，也不能被编造（仓内 547 原则：
//
//	宁可留空，不用兜底值）。
func sanitizeIdentity(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > maxIdentityLength {
		out = out[:maxIdentityLength]
	}
	return strings.TrimSpace(out)
}

// Namespace 返回该身份在记忆体系里的命名空间。
//
// ★ 与 domains/memory.UserID 同构（t:<tenant>:k:<key>:<task>），
//
//	保证「网关 PG 的层」与「Memora 的层」落在**同一个命名空间**下，
//	否则跨层检索会串租户/串任务。
func (id Identity) Namespace() string {
	task := id.Task
	if task == "" {
		task = id.Session
	}
	if task == "" {
		return ""
	}
	if id.TenantID == "" || id.TenantID == "default" {
		return "k:" + strconv.Itoa(id.APIKeyID) + ":" + task
	}
	return "t:" + id.TenantID + ":k:" + strconv.Itoa(id.APIKeyID) + ":" + task
}

// SameNamespace 判断两个身份是否落在同一记忆命名空间。
func (id Identity) SameNamespace(other Identity) bool {
	a, b := id.Namespace(), other.Namespace()
	return a != "" && a == b
}
