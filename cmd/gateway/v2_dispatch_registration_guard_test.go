package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // 测试桩需要 AnalysisEvent
)

// ══════════════════════════════════════════════════════════════════════
// 2026-11-18 V2 开关 boot panic 根修的钉测（245 生产实证的缺陷）。
//
// 缺陷：LLM_GATEWAY_USE_V2_PIPELINE=true 时，main.go 先裸注册 4 个 v1
// chat 端点、V2 块又注册同路径包装版 —— stdlib http.ServeMux 对同一路径
// 重复注册是 panic（"multiple registrations"），没有「后注册者获胜」
// 语义。旧注释声称的「后者获胜」是错误认知，导致该开关从未在真实 boot
// 里跑通过（单测不 boot 完整 main，一开就在生产炸）。
//
// 本文件两层守卫：
//   1. TestStdServeMuxDuplicateRegistrationPanics —— 钉「机制」：
//      stdlib ServeMux 重复注册必然 panic（修法必须绕开重复，而不是
//      依赖注册顺序）。
//   2. TestV1ChatRegistrationGuardedByV2Flag —— 钉「接线」：main.go 源
//      形状上，4 个裸 v1 注册必须位于 `if !v2DispatchEnabled {` 守卫
//      块内（V2 块的包装注册仍在）。谁把守卫拿掉，这里当场红。
// ══════════════════════════════════════════════════════════════════════

// TestStdServeMuxDuplicateRegistrationPanics 钉住 stdlib 的机制事实：
// 同一路径注册两次 = panic。这个断言的存在意义是让「靠注册顺序让后者
// 覆盖前者」这类想法在代码评审里有一个可以指向的红灯。
func TestStdServeMuxDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected stdlib http.ServeMux to panic on duplicate registration; " +
				"if stdlib semantics changed, re-evaluate the v1/v2 registration split in main.go")
		}
		msg := fmt.Sprint(r)
		// Go 各版本文案不同：旧版 "multiple registrations for ..."，
		// 新版 "pattern ... conflicts with pattern ..."（panic 值也不
		// 一定是 string）。核心事实是 panic 本身发生，文案只做宽松锚定。
		if !strings.Contains(msg, "multiple registrations") && !strings.Contains(msg, "conflicts with pattern") {
			t.Fatalf("panic message changed: %v", r)
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(http.ResponseWriter, *http.Request) {})
	// 第二次注册同一路径——生产 panic 的最小复现。
	mux.HandleFunc("/v1/chat/completions", func(http.ResponseWriter, *http.Request) {})
}

// TestV1ChatRegistrationGuardedByV2Flag 钉 main.go 的接线形状：
//
//  1. `if !v2DispatchEnabled {` 守卫存在；
//  2. 四个裸 v1 注册（mux.Handle("/v1/chat/completions", chatRouteHandler)
//     等）出现在守卫**之后**、守卫块的闭合花括号之前；
//  3. V2 块的包装注册（v2DispatchHandler(v2Deps, ...)）仍存在（4 条）。
func TestV1ChatRegistrationGuardedByV2Flag(t *testing.T) {
	mainGo := readMainGoSource(t)

	guardIdx := strings.Index(mainGo, "if !v2DispatchEnabled {")
	if guardIdx < 0 {
		t.Fatal("main.go: guard `if !v2DispatchEnabled {` missing — the 4 plain v1 chat " +
			"registrations would double-register with the v2 wrappers and panic at boot " +
			"(stdlib ServeMux has no last-writer-wins; 2026-11-18 production incident)")
	}

	plainRegs := []string{
		`mux.Handle("/v1/chat/completions", chatRouteHandler)`,
		`mux.Handle("/v1/completions", chatRouteHandler)`,
		`mux.Handle("/v1/messages", messagesRouteHandler)`,
		`mux.Handle("/v1/responses", responsesRouteHandler)`,
	}
	wrappedRegs := []string{
		`mux.Handle("/v1/chat/completions", v2DispatchHandler(v2Deps, chatRouteHandler))`,
		`mux.Handle("/v1/completions", v2DispatchHandler(v2Deps, chatRouteHandler))`,
		`mux.Handle("/v1/messages", v2DispatchHandler(v2Deps, messagesRouteHandler))`,
		`mux.Handle("/v1/responses", v2DispatchHandler(v2Deps, responsesRouteHandler))`,
	}

	// 守卫块的大致边界：从守卫开始找第一个只含 `}` 的行（闭合一层）。
	guardEnd := indexClosingBrace(mainGo, guardIdx)
	if guardEnd < 0 || guardEnd <= guardIdx {
		t.Fatalf("main.go: could not locate the closing brace of the !v2DispatchEnabled guard (guardIdx=%d)", guardIdx)
	}

	for _, reg := range plainRegs {
		idx := strings.Index(mainGo, reg)
		if idx < 0 {
			t.Fatalf("main.go: plain registration %q not found — registration structure moved; re-evaluate this guard", reg)
		}
		if idx < guardIdx || idx > guardEnd {
			t.Fatalf("main.go: plain registration %q at offset %d is OUTSIDE the `if !v2DispatchEnabled` block [%d,%d) — "+
				"it would double-register with the v2 wrapper and panic at boot when LLM_GATEWAY_USE_V2_PIPELINE=true",
				reg, idx, guardIdx, guardEnd)
		}
	}

	for _, reg := range wrappedRegs {
		if !strings.Contains(mainGo, reg) {
			t.Fatalf("main.go: wrapped registration %q not found — the v2 block must still register the wrapped handlers", reg)
		}
	}
}

// TestPromptInjectionStageRegisteredBeforeProviderDiscovery 钉 main_pipeline.go
// 的阶段注册序：管线按**注册序**执行（Phase 只是标签）。PI 检测阶段必须排在
// security 与 provider_discovery **两者之前**——
//
//	· provider_discovery 挂在内存 demo 凭据仓上、对真实模型恒失败 ⇒
//	  wrapper 回落 v1 ⇒ 排在其后的阶段对真实流量不可达；
//	· security.check 对注入载荷返回阶段错误（critical threat）⇒ 串行中断 ⇒
//	  排在其后的阶段对注入载荷不可达。
//
// 两者任一不满足 ⇒ prompt_injection_detections 恒空（245 生产实证）。
func TestPromptInjectionStageRegisteredBeforeProviderDiscovery(t *testing.T) {
	src := readMainPipelineSource(t)

	piIdx := strings.Index(src, `Name: "governance_prompt_injection"`)
	if piIdx < 0 {
		t.Fatal("main_pipeline.go: stage 'governance_prompt_injection' registration not found")
	}
	for _, earlier := range []string{`Name: "security"`, `Name: "provider_discovery"`} {
		idx := strings.Index(src, earlier)
		if idx < 0 {
			t.Fatalf("main_pipeline.go: stage %s registration not found", earlier)
		}
		if piIdx > idx {
			t.Fatalf("main_pipeline.go: 'governance_prompt_injection' (offset %d) is registered AFTER "+
				"%s (offset %d) — the pipeline runs stages in REGISTRATION order; security aborts on "+
				"injection payloads and provider_discovery fails against the in-memory demo credential "+
				"store for every real model (wrapper falls through to v1), so the PI hook would never "+
				"execute for real traffic", piIdx, earlier, idx)
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

func readMainPipelineSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("main_pipeline.go")
	if err != nil {
		t.Fatalf("read main_pipeline.go: %v", err)
	}
	return string(data)
}

// indexClosingBrace 从 openIdx（指向 `if ... {` 行）出发，按花括号深度
// 找到配对的 `}`，返回其在源中的字节偏移。
func indexClosingBrace(src string, openIdx int) int {
	depth := 0
	started := false
	for i := openIdx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return i
			}
		}
	}
	return -1
}

func readMainGoSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	return string(data)
}

// ══════════════════════════════════════════════════════════════════════
// v2ShutdownPipeline 不得关闭共享主 pool 的行为钉测（2026-11-18 根修）。
//
// 生产形态：boot 期 panic 展开 → defer v2ShutdownPipeline → 旧代码调
// deps.Publisher.Close() → PGPublisher.Close 关掉**共享主 pool** →
// pgxpool(puddle).Close 等待在途连接归还 → 卡死 → panic 冻结在 defer
// 里 → 进程僵尸化（监听从未绑定、systemd 不拉起）。
// 钉测：传入带计数桩的 Publisher，断言 Close **没有被调用**。
// ══════════════════════════════════════════════════════════════════════

type recordingPublisher struct {
	closed int
}

func (p *recordingPublisher) Publish(ctx context.Context, evt analysis.AnalysisEvent) error {
	return nil
}

func (p *recordingPublisher) Close() error {
	p.closed++
	return nil
}

func TestV2ShutdownPipelineDoesNotCloseSharedPoolPublisher(t *testing.T) {
	pub := &recordingPublisher{}
	deps := &v2DispatchDeps{Publisher: pub}
	v2ShutdownPipeline(deps)
	if pub.closed != 0 {
		t.Fatalf("v2ShutdownPipeline called Publisher.Close (%d times) — the V2 publisher is built "+
			"on the SHARED main pool (main.go: bus.NewPGPublisher(pool, ...)); closing it deadlocks "+
			"the shutdown path when in-flight connections are held (2026-11-18 zombie-process incident)",
			pub.closed)
	}
}

// ══════════════════════════════════════════════════════════════════════
// closeAnalysisSQLDBBounded 的行为钉测。
//
// 现状事实（变异实测）：stdlib *sql.DB.Close 不等待在途连接归还；这里的
// 有界包装是**纵深防御**——若未来换成会阻塞的驱动实现（如直连 pgxpool），
// 关闭路径仍有上限，不会把 shutdown 卡成僵尸。
// ══════════════════════════════════════════════════════════════════════

// stubCon 是一个永不主动归还的最小 driver.Conn。
type stubCon struct{}

func (stubCon) Prepare(query string) (driver.Stmt, error) { return nil, errUnsupported }
func (stubCon) Close() error                              { return nil }
func (stubCon) Begin() (driver.Tx, error)                 { return nil, errUnsupported }

type stubDriver struct{ conns chan *sql.DB }

func (d stubDriver) Open(name string) (driver.Conn, error) { return stubCon{}, nil }

var errUnsupported = &stubError{}

type stubError struct{}

func (*stubError) Error() string { return "stub: unsupported" }

func init() {
	sql.Register("v2shutdown-stub", stubDriver{})
}

func TestCloseAnalysisSQLDBBoundedReturnsDespiteHeldConn(t *testing.T) {
	db, err := sql.Open("v2shutdown-stub", "")
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	defer db.Close()

	// 拿走一条连接且永不归还——*sql.DB.Close 会等它。
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	_ = conn // 故意不释放

	done := make(chan struct{})
	go func() {
		defer close(done)
		closeAnalysisSQLDBBounded(db, 300*time.Millisecond)
	}()
	select {
	case <-done:
		// 预期路径：有界返回。
	case <-time.After(5 * time.Second):
		t.Fatal("closeAnalysisSQLDBBounded did not return within 5s while a connection was held — " +
			"the bounded close regressed to unbounded (boot-failure zombie mode)")
	}
}

func TestCloseAnalysisSQLDBBoundedNilSafe(t *testing.T) {
	// nil 直接返回（不 panic、不超时）。
	closeAnalysisSQLDBBounded(nil, 100*time.Millisecond)
}
