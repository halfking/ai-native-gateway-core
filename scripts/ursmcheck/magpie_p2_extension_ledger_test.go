package ursm_test

// magpie_p2_extension_ledger_test.go —— P2-2「扩展点台账」的**跨来源**门。
//
// # 这道门补的是哪块缺口
//
// 计划书 §6-P2-2：一份「扩展点台账」（调用次数 / p99 / 失败数 / 超时数 / 最近错误），
// **admin 可查**。
//
// ★ 本仓的台账是**两半合成的**：
//
//	静态半 = `internal/extpoints` 的裁决表（`Production` 来自**源码扫描**）；
//	运行期半 = `internal/extpointledger` 的计数器（来自**运行期计数**）。
//
// ⇒ ★ 所以真正要守的不是「计数器算得对」（那是包内门），而是
// ★ **两份来源对不对得上**、以及**接线通没通**。
//
// # 判据清单
//
//	① **机制 ID 集合单一来源**：裁决表的 ID 与台账能认的 ID **完全一致**
//	   （⇒ 不会有「台账里有、裁决表里没有」的孤立行）
//	② ★ **两份来源对拍**：裁决表说 `Production=false` ⇒ 台账调用次数必须为 0；
//	   裁决表说在生产 ⇒ 台账里**必须有**记录（接线通了）
//	③ ★ **接线真的通**：**真的跑一次**响应拦截链 ⇒ 默认台账的计数**涨**
//	   （⇒ 抓「契约好看、链路不通」）
//	④ **admin 路由真的注册了**（扫描注册点）
//	⑤ ★ 台账只认裁决表里的机制（拼错的 ID 不会凭空长出一行）
//
// # ★ 被测集合与期望都不来自被测对象
//
// 期望值是本文件里的**字面量**（路由路径、字段名、机制 ID 列表）⇒ 改实现改不掉断言。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/internal/extpointledger"
	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// magpieP2LedgerRepoRoot 返回仓根目录。
func magpieP2LedgerRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// magpieP2LedgerMechanismIDs 返回裁决表里的机制 ID 集合。
func magpieP2LedgerMechanismIDs() map[string]bool {
	out := map[string]bool{}
	for _, m := range extpoints.Mechanisms() {
		out[m.ID] = true
	}
	return out
}

// TestMagpieP2ExtensionLedgerIDsHaveSingleSource ① + ⑤：
// 裁决表的机制 ID 集合 = 台账允许的 ID 集合；且台账**只认**这些 ID。
func TestMagpieP2ExtensionLedgerIDsHaveSingleSource(t *testing.T) {
	known := magpieP2LedgerMechanismIDs()
	if len(known) == 0 {
		t.Fatal("裁决表里没有任何机制 ⇒ 判据失效")
	}
	// ★ 台账的每一行都必须来自裁决表（⇒ 没有孤立 ID）。
	ledger := extpointledger.New()
	for id := range known {
		ledger.Record(id, extpointledger.OutcomeOK, 1_000_000, nil)
	}
	// ★ 台账只认允许集合里的 ID ⇒ 拼错的 ID 不会长出台账行。
	if extpointledger.KnownMechanism(known, "definitely_not_a_mechanism") {
		t.Error("台账接受了裁决表之外的机制 ID")
	}
	if !extpointledger.KnownMechanism(known, extpoints.IDResponseIntercept) {
		t.Error("台账拒绝了裁决表里的机制 ID")
	}
	// ★ 裁决表的 ID 与常量列表必须一一对应（防「加了机制忘了更新 ID 常量」）。
	for _, id := range []string{
		extpoints.IDHTTPMiddlewareChain,
		extpoints.IDRequestPipeline,
		extpoints.IDHookRegistry,
		extpoints.IDHookPhaseVocabulary,
		extpoints.IDExecutorHooks,
		extpoints.IDResponseIntercept,
	} {
		if !known[id] {
			t.Errorf("裁决表里没有常量 %s 指名的机制", id)
		}
	}
}

// TestMagpieP2ExtensionLedgerCrossChecksProductionFlag ②：
// ★ 两份来源对拍。
func TestMagpieP2ExtensionLedgerCrossChecksProductionFlag(t *testing.T) {
	known := magpieP2LedgerMechanismIDs()
	ledger := extpointledger.New()
	produced := 0
	for _, m := range extpoints.Mechanisms() {
		if m.Production {
			produced++
			ledger.Record(m.ID, extpointledger.OutcomeOK, 1_000_000, nil)
		}
	}
	if produced == 0 {
		t.Fatal("裁决表里没有任何在生产的机制 ⇒ 判据失效")
	}
	for _, row := range extpointledger.BuildRows(ledger) {
		if !known[row.ID] {
			t.Errorf("台账出现了裁决表之外的机制 %s", row.ID)
		}
		if !row.Production && row.Calls != 0 {
			t.Errorf("机制 %s：裁决表说不在生产，台账却有 %d 次调用 ⇒ 两份来源分家",
				row.ID, row.Calls)
		}
		if row.Production && !row.Recorded {
			t.Errorf("机制 %s：裁决表说在生产，台账里却没有记录 ⇒ 接线可能没通", row.ID)
		}
		// ★ 运行期半必须**真的**进到合并后的行里
		//   （否则「合并」退化成「只留静态半」⇒ 台账页面的数字永远是 0）。
		// ⇒ 这条是 2026-10-24 变异 M5 补上的：初版门只查「两份来源分家」，
		//   查不出「运行期半被整段丢弃」。
		if row.Recorded && row.Calls == 0 {
			t.Errorf("机制 %s：台账里有记录，合并后的行 calls 却为 0 ⇒ 运行期半没进合并", row.ID)
		}
	}
	// ★ 「裁决表说在生产、但**没接台账**」必须在读端**显式可见**
	//   （否则读端分不清「这个扩展点还没接台账」与「它没有流量」）。
	//
	// ★★ 判据用 `Wired`（**静态**）而不是 `Recorded`（**运行期**）：
	//   ⇒ 空台账（本次进程内零流量）下，**已接台账**的机制 `Unwired` 必须是 false
	//   ⇒ ★ 否则「刚启动还没来请求」会被误标成「未接线」（★ 本条就是那道自查修掉的缺陷）。
	empty := extpointledger.BuildRows(extpointledger.New())
	for _, row := range empty {
		want := row.Production && !row.Wired
		if row.Unwired != want {
			t.Errorf("机制 %s：空台账下 unwired = %v，want %v（production=%v wired=%v）",
				row.ID, row.Unwired, want, row.Production, row.Wired)
		}
	}
}

// magpieP2LedgerProbeInterceptor 是一个恒失败的拦截器（用于探测接线）。
type magpieP2LedgerProbeInterceptor struct{}

func (magpieP2LedgerProbeInterceptor) InterceptNonStream(context.Context, *response.InterceptRequest) (*response.InterceptResult, error) {
	return nil, errMagpieP2Ledger
}
func (magpieP2LedgerProbeInterceptor) InterceptStreamChunk(context.Context, []byte, *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, errMagpieP2Ledger
}
func (magpieP2LedgerProbeInterceptor) InterceptStreamEnd(context.Context, *response.StreamMeta) (*response.EndResult, error) {
	return nil, errMagpieP2Ledger
}

// errMagpieP2Ledger 是探针用的错误。
var errMagpieP2Ledger = &magpieP2LedgerError{}

type magpieP2LedgerError struct{}

func (*magpieP2LedgerError) Error() string { return "magpie-p2-ledger-probe" }

// TestMagpieP2ExtensionLedgerIsActuallyWired ③：
// ★ **真的跑一次**响应拦截链 ⇒ 默认台账的计数**涨**。
//
// ⇒ 这条门抓的是「契约 + 实现 + 读端都齐了、但**接线没通**」
// （台账永远是空的 ⇒ admin 页面看着正常、其实一列数据都没有）。
func TestMagpieP2ExtensionLedgerIsActuallyWired(t *testing.T) {
	ledger := extpointledger.Default()
	before, _ := ledger.Entry(extpoints.IDResponseIntercept)

	chain := response.NewInterceptorChain(magpieP2LedgerProbeInterceptor{})
	// ★ 失败会被链吞掉（既有语义）⇒ 整体不报错，但**必须**被记账。
	if _, err := chain.InterceptNonStream(context.Background(), &response.InterceptRequest{}); err != nil {
		t.Fatalf("链不应整体报错（失败被吞是既有语义）：%v", err)
	}

	after, ok := ledger.Entry(extpoints.IDResponseIntercept)
	if !ok {
		t.Fatal("台账里没有 response_intercept ⇒ 链没记账")
	}
	if after.Calls != before.Calls+1 {
		t.Fatalf("调用次数 = %d，want %d（链没接线）", after.Calls, before.Calls+1)
	}
	if after.Failures != before.Failures+1 {
		t.Fatalf("失败数 = %d，want %d（★ 被吞掉的失败也必须被记账）",
			after.Failures, before.Failures+1)
	}
}

// TestMagpieP2ExtensionLedgerAdminRouteIsRegistered ④：
// admin 路由**真的注册了**。
func TestMagpieP2ExtensionLedgerAdminRouteIsRegistered(t *testing.T) {
	root := magpieP2LedgerRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "admin/handler.go"))
	if err != nil {
		t.Fatalf("读 admin/handler.go 失败：%v", err)
	}
	src := string(b)
	// ★ 形态：`mux.HandleFunc("<path>", h.<wrapper>(h.<handler>))`
	//   （★ handler 名后面**没有** `(` ⇒ 正则里不能多写一个 `\(`）
	re := regexp.MustCompile(
		`mux\.HandleFunc\("(/api/admin/extension-points)",\s*h\.(\w+)\(h\.(\w+)\)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("admin/handler.go 里找不到 /api/admin/extension-points 的注册点")
	}
	if m[3] != "handleExtensionPoints" {
		t.Errorf("路由挂到了 %s 上，want handleExtensionPoints", m[3])
	}
	// ★ 必须是**受保护**的路由（内部运维视图，不能是公开端点）。
	if m[2] != "superAdmin" && m[2] != "admin" {
		t.Errorf("路由的鉴权包装是 %s，want superAdmin/admin（★ 不能是公开端点）", m[2])
	}
}

// ── 接线覆盖率门（★ 源码级，不与「有没有流量」混淆）─────────────────────

// magpieP2LedgerConstants 是「机制 ID ⇒ `internal/extpoints` 里的常量名」，
// ★ **硬编码在本门里**（不从裁决表派生）⇒ 裁决表改不动断言。
//
// ⇒ 这张表同时断言了「ID 与常量名的对应关系」本身：
// 若某个常量被改名 ⇒ 这里对不上 ⇒ 生产代码里也匹配不到 ⇒ 门红。
var magpieP2LedgerConstants = map[string]string{
	"http_middleware_chain": "IDHTTPMiddlewareChain",
	"request_pipeline":      "IDRequestPipeline",
	"hook_registry":         "IDHookRegistry",
	"hook_phase_vocabulary": "IDHookPhaseVocabulary",
	"executor_hooks":        "IDExecutorHooks",
	"response_intercept":    "IDResponseIntercept",
}

// TestMagpieP2EveryProductionMechanismIsWiredToLedger ⑤ ★：
// 裁决表说**在生产**的机制，生产代码里必须有**记账调用**。
//
// ★ 为什么是**源码级**而不是运行期：
// 运行期只能看到「本次进程内有没有流量」⇒ 那把「**没接线**」与
// 「**没流量**」混成了一件事（§3.13.6 的三种成因）⇒ ★ 必须分开放。
// ⇒ 源码级判据只看「**记账调用点存在与否**」⇒ 与流量无关。
//
// ★ 扫描面：全仓**非测试** .go 文件，**排除裁决表自身**
// （`internal/extpoints` 是**被审对象**，不能同时是**证人**——
//
//	同 LESSONS §355 / §3.14）。
func TestMagpieP2EveryProductionMechanismIsWiredToLedger(t *testing.T) {
	root := magpieP2LedgerRepoRoot(t)

	const verdictTable = "internal/extpoints/extpoints.go"

	production := 0
	for _, m := range extpoints.Mechanisms() {
		if !m.Production {
			continue
		}
		production++
		constName, ok := magpieP2LedgerConstants[m.ID]
		if !ok {
			t.Fatalf("机制 %q 不在本门硬编码的「ID ⇒ 常量名」表里 ⇒ "+
				"要么裁决表加了新机制、要么 ID 写错了", m.ID)
		}
		// ★★ **两份来源对拍**：静态接线表（`internal/extpointledger.Wiring`）
		//   声明「接在哪个文件」⇒ 本门去**真的读那个文件**、确认它**真的用到了**
		//   该机制的常量 ⇒ ★ 声明与现实必须一致。
		loc := extpointledger.WiringLocation(m.ID)
		if loc == "" {
			t.Errorf("机制 %q 标 Production=true，但**静态接线表里没有它** ⇒ "+
				"★ 这个在产的扩展点**没接台账** ⇒ 读端那一行的计数恒为 0", m.ID)
			continue
		}
		if loc == verdictTable {
			t.Errorf("机制 %q 的接线位置指向裁决表自身 ⇒ ★ 裁决表不能自证", m.ID)
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(loc)))
		if err != nil {
			t.Errorf("机制 %q 的接线位置 %q 读不到：%v", m.ID, loc, err)
			continue
		}
		// ★ 必须是**真的用到**（`extpoints.IDXxx`）⇒ 而不是 mere mention。
		needle := "extpoints." + constName
		if !strings.Contains(string(b), needle) {
			t.Errorf("机制 %q 的接线文件 %q 里没有 %s ⇒ "+
				"★ 静态接线表与源码**不同源**（要么表写错了，要么接线被摘了）",
				m.ID, loc, needle)
		}
	}
	if production == 0 {
		t.Fatal("裁决表里没有任何在生产的机制 ⇒ 判据失效")
	}
}

// TestMagpieP2MainChainProbesAreInstalled ⑥ ★：
// `cmd/gateway/main.go` 里**两个探针都装了**（外层 + 内层）。
//
// ★ 为什么两个都要：外层量「总耗时」、内层量「业务耗时」，
// 两者相减才是**主链自身的开销** ⇒ 只装一个 ⇒ p99 口径就废了
// （要么量成 LLM 请求耗时，要么量成 0）。
func TestMagpieP2MainChainProbesAreInstalled(t *testing.T) {
	root := magpieP2LedgerRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "cmd", "gateway", "main.go"))
	if err != nil {
		t.Fatalf("读 cmd/gateway/main.go 失败：%v", err)
	}
	src := string(b)
	for _, needle := range []string{
		"middleware.LedgerMiddleware(",
		"middleware.LedgerInnerProbe(",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("cmd/gateway/main.go 里没有 %s ⇒ 主链没接台账（★ 接线没通）", needle)
		}
	}
}
