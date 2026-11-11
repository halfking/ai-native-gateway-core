package contextagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 记忆工具名。
//
// ★ 这两个工具是**网关托管**的：模型发出调用后由网关自己执行，
//
//	不转发给上游、不需要客户端注册、也不需要 MCP 适配器。
//	（现状：全仓没有任何「模型可调用的记忆工具」——grep 证实
//	load_memory / search_memory / recall_memory 只命中 mcp/capability.go 的
//	静态能力域标签与测试桩。）
const (
	// ToolLoadStepDetail 按步骤号取回该步的完整正文（L1，精确寻址）。
	ToolLoadStepDetail = "load_step_detail"
	// ToolSearchMemory 做跨会话的长期记忆语义召回（L2）。
	ToolSearchMemory = "search_memory"
)

// ToolNames 是网关托管的记忆工具全名。
var ToolNames = []string{ToolLoadStepDetail, ToolSearchMemory}

// ToolSpec 是一个记忆工具的声明（用于注入上游请求的 tools 数组）。
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolSpecs 返回记忆工具的声明。
//
// ★ 描述文本刻意写成「什么时候该用」——这是让模型**主动**发起记忆加载的关键：
//
//	模型必须知道「有这条路可走」，否则它只会猜。
func ToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name: ToolLoadStepDetail,
			Description: "按步骤号取回该步骤的完整历史明细（工具输出、报错、文件内容）。" +
				"当你需要某个早期步骤的确切细节（路径、错误原文、命令输出）时调用；" +
				"顶层执行摘要索引只给出每步的目标与结果，细节必须用本工具取。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"step_index": map[string]any{
						"type":        "integer",
						"description": "步骤号（来自执行摘要索引里的「步N」）",
						"minimum":     1,
					},
				},
				"required":             []any{"step_index"},
				"additionalProperties": false,
			},
		},
		{
			Name: ToolSearchMemory,
			Description: "按语义检索长期记忆（跨会话的既有事实、偏好、踩过的坑）。" +
				"当当前任务的答案可能依赖历史经验时调用；结果不保证与当前任务相关。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "检索用的自然语言查询",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "返回条数上限",
						"minimum":     1,
						"maximum":     20,
					},
				},
				"required":             []any{"query"},
				"additionalProperties": false,
			},
		},
	}
}

// ToolCall 是从上游响应里识别出的一次记忆工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult 是一次记忆工具调用的结果。
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	// Loaded 记录这次加载实际取回了什么（可寻址的证据链）。
	Loaded []string `json:"loaded"`
	// Error 非空表示工具执行失败（内容仍是给模型看的说明文本）。
	Error string `json:"error,omitempty"`
}

// DetectMemoryToolCalls 从上游响应体里识别出记忆工具调用。
//
// ★ 支持两种协议：
//
//	· openai-chat      → choices[].message.tool_calls[]
//	· anthropic-messages → content[] 里 type=="tool_use" 且 name 命中记忆工具
//
// ★ 非记忆工具一律不识别（返回空）——本包只服务记忆工具，
//
//	其它工具调用必须原样透传给客户端，**不能**被网关吞掉。
func DetectMemoryToolCalls(responseBody []byte, protocol string) []ToolCall {
	if len(responseBody) == 0 {
		return nil
	}
	switch protocol {
	case "anthropic-messages":
		return detectAnthropicToolCalls(responseBody)
	default:
		return detectOpenAIToolCalls(responseBody)
	}
}

// detectOpenAIToolCalls 识别 openai-chat 形态的工具调用。
func detectOpenAIToolCalls(responseBody []byte) []ToolCall {
	var probe struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &probe); err != nil {
		return nil
	}
	var out []ToolCall
	for _, ch := range probe.Choices {
		for _, tc := range ch.Message.ToolCalls {
			if !isMemoryTool(tc.Function.Name) {
				continue
			}
			args := json.RawMessage(tc.Function.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out = append(out, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
		}
	}
	return out
}

// detectAnthropicToolCalls 识别 anthropic-messages 形态的工具调用。
func detectAnthropicToolCalls(responseBody []byte) []ToolCall {
	var probe struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &probe); err != nil {
		return nil
	}
	var out []ToolCall
	for _, blk := range probe.Content {
		if blk.Type != "tool_use" || !isMemoryTool(blk.Name) {
			continue
		}
		args := blk.Input
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		out = append(out, ToolCall{ID: blk.ID, Name: blk.Name, Arguments: args})
	}
	return out
}

// isMemoryTool 判断工具名是否是网关托管的记忆工具。
func isMemoryTool(name string) bool {
	for _, n := range ToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// ToolDeps 是执行记忆工具所需的依赖。
type ToolDeps struct {
	Identity   Identity
	StepLoader StepDetailLoader
	LongTerm   LongTermLoader
	// MaxBytes 是单次工具结果注入的字节上限（防止一次加载炸掉上下文）。
	MaxBytes int
}

// DefaultToolResultBytes 是单次工具结果的默认字节上限。
const DefaultToolResultBytes = 32 * 1024

// ServeMemoryTool 执行一次记忆工具调用。
//
// ★ 语义（对应用户流程「从网关加载记忆 → 从 memora 加载分层信息」）：
//
//	load_step_detail → 走网关 PG 的步骤明细（L1，精确寻址）
//	search_memory   → 走 Memora 的语义召回（L2）
//
// ★ 失败不抛错：工具失败也要给模型一句可读的说明，让它能换个方式继续，
//
//	而不是让整条请求挂掉。
func ServeMemoryTool(ctx context.Context, deps ToolDeps, call ToolCall) ToolResult {
	maxBytes := deps.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultToolResultBytes
	}
	res := ToolResult{ToolCallID: call.ID, Name: call.Name}

	switch call.Name {
	case ToolLoadStepDetail:
		return serveLoadStepDetail(ctx, deps, call, maxBytes)
	case ToolSearchMemory:
		return serveSearchMemory(ctx, deps, call, maxBytes)
	default:
		res.Error = "unknown memory tool"
		res.Content = fmt.Sprintf("网关不认识记忆工具 %q。可用工具：%s。", call.Name, strings.Join(ToolNames, ", "))
		return res
	}
}

// serveLoadStepDetail 执行「按步骤取明细」。
func serveLoadStepDetail(ctx context.Context, deps ToolDeps, call ToolCall, maxBytes int) ToolResult {
	res := ToolResult{ToolCallID: call.ID, Name: call.Name}

	var args struct {
		StepIndex int `json:"step_index"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil || args.StepIndex <= 0 {
		res.Error = "invalid arguments"
		res.Content = "参数错误：需要 {\"step_index\": <正整数>}。"
		return res
	}
	if deps.StepLoader == nil {
		res.Error = "step loader unavailable"
		res.Content = fmt.Sprintf("网关当前未启用步骤明细存储，无法取回步%d 的明细。", args.StepIndex)
		return res
	}

	details, err := deps.StepLoader.LoadSteps(ctx, deps.Identity, []int{args.StepIndex})
	if err != nil {
		res.Error = err.Error()
		res.Content = fmt.Sprintf("取回步%d 明细失败：%v", args.StepIndex, err)
		return res
	}
	if len(details) == 0 {
		res.Error = "step not found"
		res.Content = fmt.Sprintf("没有找到步%d 的明细（该步可能尚未落库或已过保留期）。", args.StepIndex)
		return res
	}

	text := RenderStepDetails(details)
	res.Loaded = []string{fmt.Sprintf("step:%d", args.StepIndex)}
	res.Content = clipBytes(text, maxBytes)
	return res
}

// serveSearchMemory 执行「语义召回长期记忆」。
func serveSearchMemory(ctx context.Context, deps ToolDeps, call ToolCall, maxBytes int) ToolResult {
	res := ToolResult{ToolCallID: call.ID, Name: call.Name}

	var args struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil || strings.TrimSpace(args.Query) == "" {
		res.Error = "invalid arguments"
		res.Content = "参数错误：需要 {\"query\": <非空字符串>}。"
		return res
	}
	if deps.LongTerm == nil {
		res.Error = "long-term loader unavailable"
		res.Content = "网关当前未启用长期记忆（Memora），无法做语义召回。"
		return res
	}
	ns := deps.Identity.Namespace()
	if ns == "" {
		res.Error = "no namespace"
		res.Content = "当前请求没有可用的记忆命名空间（缺任务/会话标识），无法召回长期记忆。"
		return res
	}

	topK := args.TopK
	if topK <= 0 {
		topK = 8
	}
	items, err := deps.LongTerm.Recall(ctx, ns, args.Query, topK)
	if err != nil {
		res.Error = err.Error()
		res.Content = fmt.Sprintf("长期记忆召回失败：%v", err)
		return res
	}
	if len(items) == 0 {
		res.Content = "长期记忆里没有命中相关内容。"
		if deps.Identity.MemoryNamespaceNote != "" {
			// ★ 显式带上命名空间的边界说明：否则「没命中」会被读成
			//	「记忆是空的」，而真相是「不在同一个命名空间里」。
			res.Content += " " + deps.Identity.MemoryNamespaceNote
		}
		return res
	}

	texts := make([]string, 0, len(items))
	ids := make([]string, 0, len(items))
	for _, m := range items {
		texts = append(texts, m.Text)
		ids = append(ids, "memora:"+m.ID)
	}
	sort.Strings(ids)
	res.Loaded = ids
	res.Content = clipBytes(RenderLongTerm(texts), maxBytes)
	return res
}

// clipBytes 按字节裁剪（不切断 UTF-8 边界）。
func clipBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isUTF8Boundary(s, cut) {
		cut--
	}
	return s[:cut]
}

// isUTF8Boundary 判断 s[cut] 是否落在 UTF-8 字符边界上。
func isUTF8Boundary(s string, cut int) bool {
	if cut <= 0 || cut >= len(s) {
		return true
	}
	return s[cut]&0xC0 != 0x80
}
