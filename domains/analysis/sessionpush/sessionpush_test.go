package sessionpush

// sessionpush_test.go —— 会话分析自动推送的判据（审计 G-2）。
//
// # 这道门钉什么
//
// 1. **功能关闭 ⇒ 零 HTTP 调用**（没装 sink / sink 禁用）。
// 2. **同一 (dispatch, analysis_kind) 只推一次**（重复触发不产生第二次调用）。
// 3. **memora 宕机被吞掉**：Deliver 不返回错误、不 panic ⇒ 正常网关路径
//    上不会冒出请求错误。
// 4. **dispatch_id 是租户作用域的**（契约 v1.1 §2）：dispatch_id 由幂等键头
//    推导，生产方用**它实际有的**标识拼 ⇒ 网关实际有的是
//    (tenant_id, scoped_session_id, status)。★ scoped_session_id **不含租户**
//    ⇒ 不带租户段会跨租户撞键（memora 的幂等键是全局的）⇒ 分析永远丢在门外。
//    ★ 项目作用域在契约 v1.1 里已改为 **body 顶层字段** project_id
//    （resolve-or-create），**不再**塞进 dispatch_id。
// 5. **只映射网关真的产出的字段**：sessionmeta.Result 没有 summary /
//    outcome / key_points ⇒ 载荷里那几个字段必须为空（不臆造）。
// 6. ★ **features 与 tags 不互串**（契约 v1.1 第 ② 条）：features 来自
//    sessionmeta.Result.Features；tags 来自网关 session_tags 的行。
//
// 判据纪律：第 1 条用「**计数**」断言，不接受「没报错」。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
)

// stubSink 是可计数的推送目标。
type stubSink struct {
	disabled bool
	project  string
	calls    atomic.Int32
	err      error
	last     memclient.SessionAnalysisPayload
}

func (s *stubSink) Push(ctx context.Context, p memclient.SessionAnalysisPayload) error {
	s.calls.Add(1)
	s.last = p
	return s.err
}
func (s *stubSink) Disabled() bool { return s.disabled }
func (s *stubSink) ProjectID() string {
	return s.project
}

func testResult() sessionmeta.Result {
	return sessionmeta.Result{
		SchemaVersion: sessionmeta.SchemaVersion,
		AnalysisKind:  sessionmeta.AnalysisKind,
		Status:        sessionmeta.StatusProvisional,
		Title:         "把网关的会话分析接进记忆",
		Project: sessionmeta.ProjectSignal{
			Ref: "kaixuan-1", Label: "kaixuan", Source: "rule", Confidence: 0.8, Status: "resolved",
		},
		Evidence: []sessionmeta.Evidence{{Kind: "user_text", Value: "接进记忆"}},
		Features: []sessionmeta.Feature{{Key: "title_source", Value: "last_user_message", Source: "rule", Confidence: 1}},
		WorkTypes: []sessionmeta.Classification{
			{Value: "code", Source: "rule", Confidence: 0.7},
		},
	}
}

// TestSessionPushDisabledMakesNoCall 是第 1 条。
func TestSessionPushDisabledMakesNoCall(t *testing.T) {
	// 没装 sink。
	Set(nil)
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Result: testResult()})
	if Current() != nil {
		t.Fatalf("Set(nil) 之后 Current() 仍非 nil")
	}

	// 装了但禁用。
	sink := &stubSink{disabled: true, project: "p"}
	Set(sink)
	defer Set(nil)
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Result: testResult()})
	if sink.calls.Load() != 0 {
		t.Fatalf("禁用态发起了 %d 次推送（应为 0）", sink.calls.Load())
	}
}

// TestSessionPushCallsOncePerDispatchAndKind 是第 2 条。
//
// ★ 这里断言的是**触发层**的去重：触发层每次都调 sink.Push，
//
//	「同一键只推一次」由 sink 内部（memora 客户端的 markSeen）保证。
//	⇒ 本用例断言的是：同一 (dispatch, kind) 产生**同一个 dispatch_id**
//	（否则客户端层的去重键就不同 ⇒ 会推两次）。
func TestSessionPushCallsOncePerDispatchAndKind(t *testing.T) {
	sink := &stubSink{project: "proj-1"}
	Set(sink)
	defer Set(nil)

	in := Input{TenantID: "tenant-1", SessionID: "sess-1", TaskID: "task-1", Status: sessionmeta.StatusProvisional, Result: testResult()}
	Deliver(context.Background(), in)
	first := sink.last.DispatchID
	Deliver(context.Background(), in)
	second := sink.last.DispatchID
	if first != second {
		t.Errorf("同一输入两次触发的 dispatch_id 不同：%q vs %q —— "+
			"客户端层的幂等去重键就不同 ⇒ 会推两次", first, second)
	}
	if sink.calls.Load() != 2 {
		t.Errorf("触发层调用了 %d 次（应为 2：两次触发各调一次 sink）", sink.calls.Load())
	}
	// provisional 与 final 是**两个不同的 dispatch** ⇒ 两个不同的键。
	// （否则 final 会被当成同键重放 ⇒ 409 ⇒ 最终态永远落不下去。）
	final := in
	final.Status = sessionmeta.StatusFinal
	Deliver(context.Background(), final)
	if sink.last.DispatchID == first {
		t.Errorf("final 与 provisional 的 dispatch_id 相同（%q）—— "+
			"最终态会被 memora 当成同键重放拒掉（409）", first)
	}
}

// TestSessionPushDispatchIDIsTenantScoped 是第 4 条。
//
// ★ 为什么是**租户**而不是项目：契约 v1.1 把项目作用域改成了 body 顶层字段
//
//	（resolve-or-create），而 memora 的幂等键是**全局**的 ⇒ 幂等键里必须
//	带租户，否则两个租户的同名会话会撞进同一个键（409 ⇒ 分析永远丢在门外）。
func TestSessionPushDispatchIDIsTenantScoped(t *testing.T) {
	sink := &stubSink{project: "proj-A"}
	Set(sink)
	defer Set(nil)

	Deliver(context.Background(), Input{TenantID: "tenant-A", SessionID: "s", Status: sessionmeta.StatusFinal, Result: testResult()})
	if !strings.HasPrefix(sink.last.DispatchID, "tenant-A:") {
		t.Errorf("dispatch_id = %q（必须以租户 id 开头）—— "+
			"memora 的幂等键是全局的，不带租户就会跨租户撞键", sink.last.DispatchID)
	}
	if !strings.HasSuffix(sink.last.DispatchID, "-final") {
		t.Errorf("dispatch_id = %q（必须以状态结尾）—— "+
			"provisional 与 final 是两个不同的产出，不带状态时 final 会被当成同键重放", sink.last.DispatchID)
	}
	// 换一个租户 ⇒ 键必须不同（跨租户不撞键）。
	Deliver(context.Background(), Input{TenantID: "tenant-B", SessionID: "s", Status: sessionmeta.StatusFinal, Result: testResult()})
	if !strings.HasPrefix(sink.last.DispatchID, "tenant-B:") {
		t.Errorf("dispatch_id = %q（必须以 tenant-B 开头）", sink.last.DispatchID)
	}
	// ★ 换一个**项目**不改 dispatch_id（项目作用域在 body 字段里）。
	sink.project = "proj-B"
	Deliver(context.Background(), Input{TenantID: "tenant-A", SessionID: "s", Status: sessionmeta.StatusFinal, Result: testResult()})
	if !strings.HasPrefix(sink.last.DispatchID, "tenant-A:") {
		t.Errorf("dispatch_id = %q（项目换了不该改 dispatch_id —— "+
			"项目作用域走 body 的 project_id 字段）", sink.last.DispatchID)
	}
}

// TestSessionPushSwallowsMemoraOutage 是第 3 条。
func TestSessionPushSwallowsMemoraOutage(t *testing.T) {
	sink := &stubSink{project: "p", err: context.DeadlineExceeded}
	Set(sink)
	defer Set(nil)

	// Deliver 不返回错误、不 panic —— 正常网关路径上不该冒出请求错误。
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Result: testResult()})
	if sink.calls.Load() != 1 {
		t.Errorf("sink 调用次数 = %d（应为 1）", sink.calls.Load())
	}
}

// TestSessionPushEndToEndThroughTheRealPusher 是端到端形态：
// 真的 memora 客户端 + 真的 HTTP 桩，验证「一次触发 ⇒ 一次请求」
// 与「memora 5xx 被吞掉」。
func TestSessionPushEndToEndThroughTheRealPusher(t *testing.T) {
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	pusher := memclient.NewSessionAnalysisPusher(memclient.SessionAnalysisConfig{
		BaseURL:   srv.URL,
		JWTSecret: "secret",
		ProjectID: "proj-1",
	})
	Set(pusher)
	defer Set(nil)

	in := Input{TenantID: "t", SessionID: "s", Status: sessionmeta.StatusProvisional, Result: testResult()}
	Deliver(context.Background(), in)
	Deliver(context.Background(), in)
	Deliver(context.Background(), in)
	if calls.Load() != 1 {
		t.Errorf("三次触发产生了 %d 次 HTTP 调用（应为 1 —— 同一键只推一次）", calls.Load())
	}

	// ★ 失败时**释放**去重占位 ⇒ 后续触发可以重试。
	//   （若不释放，这次分析就永远推不出去 —— 那是丢数据。
	//     成功时仍然只推一次。）
	status.Store(http.StatusInternalServerError)
	in2 := Input{TenantID: "t", SessionID: "s2", Status: sessionmeta.StatusProvisional, Result: testResult()}
	Deliver(context.Background(), in2)
	Deliver(context.Background(), in2)
	Deliver(context.Background(), in2)
	if calls.Load() != 4 {
		t.Errorf("失败后三次重试共产生 %d 次 HTTP 调用（应为 4 = 1 成功 + 3 重试）—— "+
			"失败时若不释放去重占位，这次分析就永远推不出去", calls.Load())
	}
}

// TestSessionPushPayloadDoesNotInventFields 是第 5 条。
func TestSessionPushPayloadDoesNotInventFields(t *testing.T) {
	payload, ok := BuildPayload(Input{
		TenantID: "t", SessionID: "s", TaskID: "task-1",
		Status: sessionmeta.StatusFinal, Result: testResult(),
	}, "proj-1")
	if !ok {
		t.Fatalf("BuildPayload 返回 ok=false（有标题的载荷必须有可沉淀内容）")
	}
	if payload.Summary != "" || payload.Outcome != "" || len(payload.KeyPoints) > 0 {
		t.Errorf("载荷里出现了网关**没有产出**的字段（summary/outcome/key_points）：%+v", payload)
	}
	if payload.Title != "把网关的会话分析接进记忆" {
		t.Errorf("Title = %q", payload.Title)
	}
	// ★ 引文发网关的字段名 **evidence**（grounding 是 memora 的历史别名，
	//   两者同构同义、memora 会合并去重 ⇒ 只发一份）。
	if len(payload.Evidence) != 1 || payload.Evidence[0].Value != "接进记忆" {
		t.Errorf("Evidence = %+v（应来自 sessionmeta.Evidence）", payload.Evidence)
	}
	if len(payload.Grounding) != 0 {
		t.Errorf("Grounding = %+v（只发 evidence 一份即可，两份会被 memora 合并去重）", payload.Grounding)
	}
	// ★ **features**：来自 sessionmeta.Result.Features（契约 v1.1 第 ② 条）。
	if len(payload.Features) != 1 || payload.Features[0].Key != "title_source" {
		t.Errorf("Features = %+v（应来自 sessionmeta.Features）", payload.Features)
	}
	// ★ **tags**：来自网关 session_tags 的行（**不是** features）。
	if len(payload.Tags) != 0 {
		t.Errorf("Tags = %+v（没有 session_tags 行时必须是空 —— "+
			"绝不能把 features 塞进 tags，那正是 memora 二轮审计点名的「静默贴错标签」缺陷）", payload.Tags)
	}
	// work_types：**结构化**（契约 v1.1 第 ① 条）。
	if len(payload.WorkTypes) != 1 ||
		payload.WorkTypes[0].Value != "code" ||
		payload.WorkTypes[0].Source != "rule" ||
		payload.WorkTypes[0].Confidence != 0.7 {
		t.Errorf("WorkTypes = %+v（应是结构化的 {value,source,confidence}）", payload.WorkTypes)
	}
	if payload.Project == nil || payload.Project.Ref != "kaixuan-1" {
		t.Errorf("Project = %+v（应来自 sessionmeta.ProjectSignal）", payload.Project)
	}
	if payload.Task == nil || payload.Task.ID != "task-1" {
		t.Errorf("Task = %+v", payload.Task)
	}
}

// TestSessionPushSkipsPayloadWithoutLandableContent 镜像 memora 的 Validate：
// 没有标题/摘要/结论/要点 ⇒ 不推。
func TestSessionPushSkipsPayloadWithoutLandableContent(t *testing.T) {
	sink := &stubSink{project: "p"}
	Set(sink)
	defer Set(nil)

	result := testResult()
	result.Title = ""
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Result: result})
	if sink.calls.Load() != 0 {
		t.Errorf("没有可沉淀内容的载荷推了 %d 次（应为 0）", sink.calls.Load())
	}
}

// TestSessionPushTagsComeFromSessionTags 钉住 tags 的**来源**。
//
// ★ 契约 v1.1 第 ② 条：tags = 网关 session_tags 的行；features =
//
//	sessionmeta.Result.Features。两者同构但来源不同 ⇒ 一个测试**同时**
//	提供 features 与 tags，断言它们各自落在自己的字段里、**不互串**。
func TestSessionPushTagsComeFromSessionTags(t *testing.T) {
	payload, ok := BuildPayload(Input{
		TenantID: "t", SessionID: "s", Status: sessionmeta.StatusProvisional,
		Result: testResult(),
		Tags: []Tag{
			{Key: "topic", Value: "记忆", Source: "auto", Confidence: 1},
			{Key: "intent", Value: "chat", Source: "llm", Confidence: 0.8},
		},
	}, "proj-1")
	if !ok {
		t.Fatalf("BuildPayload 返回 ok=false")
	}
	if len(payload.Tags) != 2 || payload.Tags[0].Key != "topic" {
		t.Errorf("Tags = %+v（应来自 Input.Tags，即网关 session_tags 的行）", payload.Tags)
	}
	if len(payload.Features) != 1 || payload.Features[0].Key != "title_source" {
		t.Errorf("Features = %+v（应来自 sessionmeta.Features）", payload.Features)
	}
	// 反向：features 里不许出现 tags 的 key。
	for _, f := range payload.Features {
		if f.Key == "topic" || f.Key == "intent" {
			t.Errorf("features 里混进了 tags 的内容（key=%s）—— 这就是「静默贴错标签」缺陷", f.Key)
		}
	}
}

// TestSessionPushMarksUnclassifiedProject 钉住 project.unclassified（契约 §5）。
//
// ★ 语义：「project 整个缺席」= 生产方**没报**项目信号；
//
//	「unclassified: true」= 生产方**明确判定**为未归类。
//	网关的 extractor 总是给出一个 project 字段（extractor_rules.go 的
//	extractProject 有三个分支），其中 Source="unknown" 分支就是
//	「看过并判定没有归属项目」⇒ 映射成 unclassified=true。
func TestSessionPushMarksUnclassifiedProject(t *testing.T) {
	result := testResult()
	result.Project = sessionmeta.ProjectSignal{Source: "unknown", Confidence: 0, Status: "pending"}
	payload, ok := BuildPayload(Input{
		TenantID: "t", SessionID: "s", Status: sessionmeta.StatusFinal, Result: result,
	}, "proj-1")
	if !ok {
		t.Fatalf("BuildPayload 返回 ok=false")
	}
	if payload.Project == nil || !payload.Project.Unclassified {
		t.Errorf("Project = %+v（Source=unknown ⇒ 必须标 unclassified=true）", payload.Project)
	}

	// 有项目信号时不得标 unclassified。
	payload2, ok := BuildPayload(Input{
		TenantID: "t", SessionID: "s", Status: sessionmeta.StatusFinal, Result: testResult(),
	}, "proj-1")
	if !ok {
		t.Fatalf("BuildPayload 返回 ok=false")
	}
	if payload2.Project == nil || payload2.Project.Unclassified {
		t.Errorf("Project = %+v（有项目信号时不得标 unclassified）", payload2.Project)
	}
}
