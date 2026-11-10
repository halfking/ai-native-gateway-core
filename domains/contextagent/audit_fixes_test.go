package contextagent

// 本文件是 2026-11-11 上下文智能体审计轮的**钉桩测试**。
//
// ★ 纪律：每条测试对应审计轮查出的一个**具体缺陷**，且必须能证明
//
//	「修回去就会红」（变异验证）。没有牙齿的门等于没门。
//
// ★ 审计轮查出的缺陷与本文件的对应关系：
//
//	F1  indexOfKey 不判嵌套层级  → TestAssemble_NestedMessagesKeyIsNotTouched
//	                                TestInjectToolSpecs_NestedToolsKeyIsNotTouched
//	F2  无 system 消息 ⇒ 装配静默失效 → TestAssemble_WorksWithoutSystemMessage
//	F3  submit=false 仍打上游     → TestAgentProcess_AssembleOnlyNeverSubmits
//	                                TestHandleResolve_SubmitFalseNeverSubmits
//	                                TestHandleResolve_SubmitTrueSubmitsExactlyOnce
//	F4  命名空间与写侧不同构       → （在 contextagent_test.go 里对拍写侧公式）
//	F5  L0 步号 ≠ turn_no         → TestPGIndexLoader_UsesAuthoritativeTurnNo
//	F6  回灌凭据必然是管理令牌     → TestHandleResolve_SubmitTrueNeedsModelCredential
//	                                TestHandleResolve_LoopbackCarriesModelCredential
//	F9  出参契约/编码             → TestResolveResponse_FieldsAreExplicitlyBase64
//	F10 stream:true ⇒ 回环失效    → TestForceNonStream_*
//	F12 tools 注入到空对象产生坏体 → TestInjectToolSpecs_EmptyObjectStaysValidJSON
//
// ★ 另有「结构性边界」被**显式钉住**（不是缺陷，是已登记的边界）：
//	（见 TestServeMemoryTool_SearchMemoryReportsNamespaceBoundary）

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── 计数提交器（用来数「打了几次上游」）────────────────────────────────────

// countingSubmitter 记录被调用的次数与最后一次的请求体。
type countingSubmitter struct {
	calls int
	last  []byte
	resp  []byte
}

func (c *countingSubmitter) Submit(_ context.Context, body []byte) ([]byte, error) {
	c.calls++
	c.last = append([]byte(nil), body...)
	if c.resp != nil {
		return c.resp, nil
	}
	return []byte(`{"choices":[{"message":{"content":"ok"}}]}`), nil
}

// ── F1：嵌套键不得被替换 ─────────────────────────────────────────────────

// TestAssemble_NestedMessagesKeyIsNotTouched —— 顶层 "messages" 必须被替换，
// **嵌套**的同名键必须原封不动。
//
// ★ 审计轮的缺陷：indexOfKey 只看「前一个字节是 { 或 ,」⇒ 命中嵌套键
//
//	⇒ 顶层 messages 没换（记忆层被静默丢弃），而嵌套数组被换成整份新
//	消息数组（出站体被改坏）。
func TestAssemble_NestedMessagesKeyIsNotTouched(t *testing.T) {
	body := []byte(`{"metadata":{"messages":["decoy"]},"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	out, ok := Assemble(body, []LayerContent{{Layer: LayerIndex, Text: "IDX"}})
	if !ok {
		t.Fatal("assembly must succeed")
	}

	var probe struct {
		Metadata struct {
			Messages []string `json:"messages"`
		} `json:"metadata"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("result must be valid JSON: %v", err)
	}
	if len(probe.Metadata.Messages) != 1 || probe.Metadata.Messages[0] != "decoy" {
		t.Fatalf("nested messages must stay untouched, got %+v", probe.Metadata.Messages)
	}
	if len(probe.Messages) != 2 {
		t.Fatalf("top-level messages must be replaced (1 original + 1 injected), got %d", len(probe.Messages))
	}
}

// TestInjectToolSpecs_NestedToolsKeyIsNotTouched —— 同源缺陷：工具声明
// 必须注入**顶层** tools，不得动嵌套的同名键。
func TestInjectToolSpecs_NestedToolsKeyIsNotTouched(t *testing.T) {
	body := []byte(`{"meta":{"tools":[{"name":"other"}]},"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	out, ok := InjectToolSpecs(body, "openai-chat")
	if !ok {
		t.Fatal("tool injection must succeed")
	}

	var probe struct {
		Meta struct {
			Tools []map[string]any `json:"tools"`
		} `json:"meta"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("result must be valid JSON: %v", err)
	}
	if len(probe.Meta.Tools) != 1 || probe.Meta.Tools[0]["name"] != "other" {
		t.Fatalf("nested tools must stay untouched, got %+v", probe.Meta.Tools)
	}
	if len(probe.Tools) != 2 {
		t.Fatalf("top-level tools must hold the two memory tools, got %d", len(probe.Tools))
	}
}

// ── F2：没有 system 消息时也必须装配 ─────────────────────────────────────

// TestAssemble_WorksWithoutSystemMessage —— 「首条就是 user」是最常见的
// 请求形状，必须能装配。
//
// ★ 审计轮的缺陷：splitHeadAndTail 在首条即 user 时 head 仍是 nil，
//
//	而 Assemble 用 `head == nil` 当失败标志 ⇒ 整体不装配。
func TestAssemble_WorksWithoutSystemMessage(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	out, ok := Assemble(body, []LayerContent{{Layer: LayerIndex, Text: "IDX"}})
	if !ok {
		t.Fatal("assembly must succeed without a system message")
	}
	if !strings.Contains(string(out), "IDX") {
		t.Fatalf("index layer must be injected: %s", out)
	}
	var probe struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("result must be valid JSON: %v", err)
	}
	if len(probe.Messages) != 2 {
		t.Fatalf("expected original user + injected index, got %d", len(probe.Messages))
	}
	// 装配顺序与生产 rebuilder 同序：索引在**首条 user 之前**（最上层）。
	if probe.Messages[0]["content"] != "IDX" {
		t.Fatalf("index must be placed above the first user message: %+v", probe.Messages[0])
	}
	if probe.Messages[1]["content"] != "hi" {
		t.Fatalf("first user message must be preserved: %+v", probe.Messages[1])
	}
}

// ── F3：只装配不提交 ─────────────────────────────────────────────────────

// TestAgentProcess_AssembleOnlyNeverSubmits —— AssembleOnly 形态下
// **一次上游都不能打**。
func TestAgentProcess_AssembleOnlyNeverSubmits(t *testing.T) {
	sub := &countingSubmitter{}
	agent := &Agent{Submitter: sub, InjectTools: true}

	out, err := agent.Process(context.Background(), Request{
		RequestID:    "req-assemble-only",
		Body:         []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		AssembleOnly: true,
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if sub.calls != 0 {
		t.Fatalf("assemble-only must not call upstream, got %d calls", sub.calls)
	}
	if len(out.AssembledBody) == 0 {
		t.Fatal("assemble-only must return the assembled body")
	}
	if out.Rounds != 0 {
		t.Fatalf("assemble-only must report 0 rounds, got %d", out.Rounds)
	}
	if out.Record == nil || len(out.Record.AssembledBody) == 0 {
		t.Fatal("record must carry the assembled body")
	}
	if len(out.Record.Rounds) != 0 {
		t.Fatalf("record must have no upstream rounds, got %d", len(out.Record.Rounds))
	}
}

// ── F5：L0 索引必须用权威轮次号 ──────────────────────────────────────────

// TestPGIndexLoader_UsesAuthoritativeTurnNo —— 索引 SQL 必须按 request_id
// join 到 session_turns 的权威轮次号，且两臂都要查。
//
// ★ 审计轮的缺陷：索引直接用摘要表的 step_index，而它是
//
//	「ORDER BY ts ASC LIMIT 100」的**稠密行号** ⇒ 与 turn_no 不是同一个序列
//	⇒ 模型按索引给的步号去取明细会取回别的轮。
func TestPGIndexLoader_UsesAuthoritativeTurnNo(t *testing.T) {
	db := &fakeQueryer{groups: []*fakeRows{{rows: [][]any{
		{7, "目标", "结果"},
	}}}}
	l := NewPGIndexLoader(db)

	entries, err := l.LoadIndex(context.Background(), Identity{TenantID: "t1", Session: "sess-1"})
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if len(entries) != 1 || entries[0].StepIndex != 7 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if entries[0].Goal != "目标" || entries[0].Result != "结果" {
		t.Fatalf("summary content must be preserved: %+v", entries[0])
	}

	// ★ 形状门：SQL 必须按 request_id join 到权威轮次号，且两臂都要查。
	if len(db.sqls) != 1 {
		t.Fatalf("expected exactly one query, got %d", len(db.sqls))
	}
	sql := db.sqls[0]
	for _, needle := range []string{
		"SELECT t.turn_no,",           // 投影出来的步号必须来自轮次表
		"t.request_id = s.request_id", // 按 request_id 对齐
		"session_turns_hot",           // 热臂
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("index SQL must contain %q:\n%s", needle, sql)
		}
	}
	// 两臂都要查（写入只落 hot，促销搬进冷表）⇒ 恰好两处 public.session_turns。
	if n := strings.Count(sql, "public.session_turns"); n != 2 {
		t.Fatalf("index SQL must read both session_turns arms, got %d occurrences:\n%s", n, sql)
	}
	// ★ 反向判据：整条 SQL 都不得再出现 step_index（它是摘要表的稠密行号，
	//	不是轮次号）。这一条必须严到「ORDER BY 里也不许有」——
	//	否则把投影改回 step_index 而保留 ORDER BY t.turn_no 的半吊子变异
	//	会逃过门（变异验证实测过：宽松版门放过了它）。
	if strings.Contains(sql, "step_index") {
		t.Fatalf("index SQL must not read step_index as the step number:\n%s", sql)
	}
}

// ── F6：回灌凭据 ─────────────────────────────────────────────────────────

// TestHandleResolve_SubmitTrueNeedsModelCredential —— submit=true 必须
// 要求模型面凭据（否则回灌拿管理令牌打数据面 ⇒ 401 ⇒ 500）。
func TestHandleResolve_SubmitTrueNeedsModelCredential(t *testing.T) {
	sub := &countingSubmitter{}
	svc := &Service{Agent: &Agent{Submitter: sub}}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":true}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer ops-admin-token")

	_, status, err := svc.HandleResolve(context.Background(), r)
	if err == nil {
		t.Fatal("submit=true without a model credential must fail loudly")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if sub.calls != 0 {
		t.Fatalf("no upstream call may happen, got %d", sub.calls)
	}
}

// deriverSubmitter 记录回灌时用的 Authorization。
type deriverSubmitter struct {
	countingSubmitter
	gotAuth string
	gotID   Identity
}

func (d *deriverSubmitter) ForRequest(identity Identity, authHeader string) Submitter {
	d.gotAuth = authHeader
	d.gotID = identity
	return d
}

// TestHandleResolve_LoopbackCarriesModelCredential —— 回灌必须带**模型面
// 凭据**，不是管理令牌。
func TestHandleResolve_LoopbackCarriesModelCredential(t *testing.T) {
	sub := &deriverSubmitter{}
	svc := &Service{
		Agent:           &Agent{Submitter: sub},
		ModelCredential: "sk-model-credential",
	}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":true}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer ops-admin-token")
	r.Header.Set("X-Gw-Tenant-Id", "tenant-1")

	resp, status, err := svc.HandleResolve(context.Background(), r)
	if err != nil {
		t.Fatalf("resolve: %v (status=%d)", err, status)
	}
	if sub.gotAuth != "Bearer sk-model-credential" {
		t.Fatalf("loopback must carry the model credential, got %q", sub.gotAuth)
	}
	if sub.calls != 1 {
		t.Fatalf("submit=true must call upstream exactly once, got %d", sub.calls)
	}
	if resp.ResponseBodyBase64 == "" {
		t.Fatal("response body must be returned")
	}
}

// TestHandleResolve_RequestCredentialNeedsExplicitOptIn —— 调用方自选凭据
// 必须**显式开启**（默认关：凭据决定命名空间 = 放大读取面）。
func TestHandleResolve_RequestCredentialNeedsExplicitOptIn(t *testing.T) {
	sub := &deriverSubmitter{}
	svc := &Service{Agent: &Agent{Submitter: sub}, ModelCredential: "sk-configured"}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":true}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
	r.Header.Set("X-Model-Key", "sk-client-supplied")

	// 默认关：调用方自带的凭据必须被**忽略**（用运维配置的那把）。
	if _, _, err := svc.HandleResolve(context.Background(), r); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sub.gotAuth != "Bearer sk-configured" {
		t.Fatalf("a client-supplied credential must be ignored by default, got %q", sub.gotAuth)
	}

	// 显式开启：才允许调用方自选。
	svc.AllowRequestCredential = true
	r2 := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
	r2.Header.Set("X-Model-Key", "sk-client-supplied")
	if _, _, err := svc.HandleResolve(context.Background(), r2); err != nil {
		t.Fatalf("resolve with explicit opt-in: %v", err)
	}
	if sub.gotAuth != "Bearer sk-client-supplied" {
		t.Fatalf("opt-in must honour the request credential, got %q", sub.gotAuth)
	}
}

// TestHandleResolve_SubmitFalseNeverSubmits —— submit=false **一次上游都不能打**。
//
// ★ 审计轮的缺陷：submit 判断在 Process **之后** ⇒ submit=false 也打了
//
//	一次上游（非预期调用 + 计费）。
func TestHandleResolve_SubmitFalseNeverSubmits(t *testing.T) {
	sub := &countingSubmitter{}
	svc := &Service{Agent: &Agent{Submitter: sub}, ModelCredential: "sk-model-credential"}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":false}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))

	resp, status, err := svc.HandleResolve(context.Background(), r)
	if err != nil {
		t.Fatalf("resolve: %v (status=%d)", err, status)
	}
	if sub.calls != 0 {
		t.Fatalf("submit=false must not call upstream, got %d calls", sub.calls)
	}
	if resp.BodyBase64 == "" {
		t.Fatal("submit=false must return the assembled body")
	}
	if resp.Rounds != 0 {
		t.Fatalf("submit=false must report 0 rounds, got %d", resp.Rounds)
	}
}

// TestHandleResolve_SubmitTrueSubmitsExactlyOnce —— submit=true 只打**一次**上游。
//
// ★ 审计轮的缺陷：第一次 Process（未判 submit）已经打了一次 ⇒ 双倍成本。
func TestHandleResolve_SubmitTrueSubmitsExactlyOnce(t *testing.T) {
	sub := &countingSubmitter{}
	svc := &Service{Agent: &Agent{Submitter: sub}, ModelCredential: "sk-model-credential"}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":true}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))

	if _, _, err := svc.HandleResolve(context.Background(), r); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sub.calls != 1 {
		t.Fatalf("submit=true must call upstream exactly once, got %d", sub.calls)
	}
}

// TestHandleResolve_CredentialBearerPrefixIsStripped —— 凭据带 "Bearer "
// 前缀时必须剥掉，否则哈希与写侧对不上 ⇒ 命名空间错位。
func TestHandleResolve_CredentialBearerPrefixIsStripped(t *testing.T) {
	sub := &deriverSubmitter{}
	svc := &Service{
		Agent:           &Agent{Submitter: sub},
		ModelCredential: "Bearer sk-with-prefix",
	}

	body := `{"body":"{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}","submit":true}`
	r := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
	if _, _, err := svc.HandleResolve(context.Background(), r); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sub.gotAuth != "Bearer sk-with-prefix" {
		t.Fatalf("the prefix must be stripped exactly once, got %q", sub.gotAuth)
	}
}

// ── F9：出参契约 ─────────────────────────────────────────────────────────

// TestResolveResponse_FieldsAreExplicitlyBase64 —— 出参字段名必须显式
// 写明 base64（否则客户端会把 base64 串当请求体直接转发）。
func TestResolveResponse_FieldsAreExplicitlyBase64(t *testing.T) {
	raw, err := json.Marshal(ResolveResponse{BodyBase64: "e30=", ResponseBodyBase64: "e30="})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"body_b64"`) {
		t.Fatalf("body field must be named body_b64: %s", raw)
	}
	if !strings.Contains(string(raw), `"response_body_b64"`) {
		t.Fatalf("response body field must be named response_body_b64: %s", raw)
	}
	if strings.Contains(string(raw), `"body":`) {
		t.Fatalf("ambiguous body field must be gone: %s", raw)
	}
}

// ── F10：强制非流式 ──────────────────────────────────────────────────────

// TestForceNonStream_RewritesTopLevelStream —— 顶层 stream 必须被改写。
func TestForceNonStream_RewritesTopLevelStream(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out, ok := forceNonStream(body)
	if !ok {
		t.Fatal("stream must be rewritten")
	}
	if !strings.Contains(string(out), `"stream":false`) {
		t.Fatalf("stream must become false: %s", out)
	}
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("result must be valid JSON: %v", err)
	}
}

// TestForceNonStream_IgnoresNestedStream —— 嵌套的 stream 不得被改。
func TestForceNonStream_IgnoresNestedStream(t *testing.T) {
	body := []byte(`{"meta":{"stream":true},"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, ok := forceNonStream(body)
	if ok {
		t.Fatalf("no top-level stream ⇒ no rewrite, got %s", out)
	}
	if string(out) != string(body) {
		t.Fatal("body must stay byte-identical")
	}
}

// TestForceNonStream_AlreadyFalseIsNoop —— 已经是 false 时不重复改写。
func TestForceNonStream_AlreadyFalseIsNoop(t *testing.T) {
	body := []byte(`{"model":"m","stream":false}`)
	out, ok := forceNonStream(body)
	if ok {
		t.Fatalf("already false ⇒ no rewrite, got %s", out)
	}
	if string(out) != string(body) {
		t.Fatal("body must stay byte-identical")
	}
}

// ── F12：工具注入到空对象不得产出坏 JSON ────────────────────────────────

// TestInjectToolSpecs_EmptyObjectStaysValidJSON —— 注入到 `{}` 上不得
// 产出 `{"tools":[...],}` 这种带尾逗号的非法 JSON。
func TestInjectToolSpecs_EmptyObjectStaysValidJSON(t *testing.T) {
	out, ok := InjectToolSpecs([]byte(`{}`), "openai-chat")
	if !ok {
		t.Fatal("injection must succeed on an empty object")
	}
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("result must be valid JSON: %v (%s)", err, out)
	}
	if len(probe["tools"].([]any)) != 2 {
		t.Fatalf("two memory tools expected, got %+v", probe["tools"])
	}
}

// ── F11：L1 明细按需加载（不进装配），但必须能按权威步号取回 ───────────

// TestL1DetailIsOnDemandNotAutoLoaded —— L1 明细**不进装配**，
// 但必须能经记忆工具按**权威步号**精确取回。
//
// ★ 这条契约是审计轮的处置结论（不是「实现没来得及」）：
//
//	自动装载 L1 会把「request_delta 可能是整份历史重复」这个已知缺陷放大
//	（同一份历史被注入 N 次）⇒ 上下文预算爆。
//	⇒ L1 走工具按需取；L0 索引给出权威步号，两者对得上才是闭环。
func TestL1DetailIsOnDemandNotAutoLoaded(t *testing.T) {
	agent := &Agent{
		IndexLoader: &fakeIndexLoader{entries: []IndexEntry{
			{StepIndex: 7, Goal: "目标", Result: "结果"},
		}},
		StepLoader: &fakeStepLoader{details: map[int][]map[string]any{
			7: {{"role": "user", "content": "detail-7"}},
		}},
		Submitter: &countingSubmitter{},
	}

	out, err := agent.Process(context.Background(), Request{
		RequestID: "req-l1",
		Body:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	for _, l := range out.Layers {
		if l.Layer == LayerStepDetail {
			t.Fatalf("L1 must not be auto-loaded into the assembly: %+v", l)
		}
	}
	// 索引层必须在，且步号是权威的 7。
	var idx *LayerContent
	for i := range out.Layers {
		if out.Layers[i].Layer == LayerIndex {
			idx = &out.Layers[i]
		}
	}
	if idx == nil {
		t.Fatal("index layer must be loaded")
	}
	if !strings.Contains(idx.Text, "步7") {
		t.Fatalf("index must carry the authoritative step number: %q", idx.Text)
	}

	// 工具按步号取回那一步。
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity:   Identity{Session: "sess-1"},
		StepLoader: agent.StepLoader,
	}, ToolCall{ID: "c1", Name: ToolLoadStepDetail, Arguments: json.RawMessage(`{"step_index":7}`)})
	if res.Error != "" {
		t.Fatalf("load step detail: %s", res.Error)
	}
	if !strings.Contains(res.Content, "detail-7") {
		t.Fatalf("tool must return the requested step: %q", res.Content)
	}
}

// ── 结构性边界（显式钉住，不是缺陷）──────────────────────────────────────

// TestServeMemoryTool_SearchMemoryReportsNamespaceBoundary —— 命名空间
// 不重叠时，「没有命中」必须**带上边界说明**，不能只回一句空话。
//
// ★ 否则读数会被读成「记忆里没有」，而真相是「压根不在同一个命名空间」。
func TestServeMemoryTool_SearchMemoryReportsNamespaceBoundary(t *testing.T) {
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity: Identity{
			MemoryNamespace:     "t:t:k:1:tk",
			MemoryNamespaceNote: "命名空间由 MCP 凭证派生，与数据面不重叠。",
		},
		LongTerm: &fakeLongTerm{},
	}, ToolCall{ID: "c1", Name: ToolSearchMemory, Arguments: json.RawMessage(`{"query":"x"}`)})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "命名空间由 MCP 凭证派生") {
		t.Fatalf("empty result must carry the namespace boundary note: %q", res.Content)
	}
}
