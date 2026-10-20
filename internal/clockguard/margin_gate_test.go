package clockguard

// margin_gate_test.go —— R68 轮新增：**判别型下界的余量棘轮门**。
//
// ★ 这道门补的是 R66 轮留下的那个洞。
//
// R66 轮把全仓 53 处时钟判据按「判别职责」分型后，发现「判别型下界」（判
// 「不能太快」）只有 8 处产品判据（+ 3 处扫描器自己的夹具），并把其中除
// distlock 外的 7 处登记为「**已分析、未实测**」—— 依据是**读实现推导**
// （定时器只晚不早 ⇒ 下界恒成立），不是实测。
//
// ⇒ R68 轮逐处实测了自然值（探针复制原测试的等待序列，测完即删），
// 数据落在 measured_margins.tsv。
//
// ⇒ 而 distlock 那处已经证明：**余量按构造接近零时，跑够多次也不会红**
// ⇒ 「包级全绿」证明不了余量 ⇒ 门必须能抓住「下界被改大到贴近自然值」。
//
// 门的三条判据：
//  1. **登记册严格相等**（双向）：扫描出的判别型下界集合必须与台账逐条相等
//     ⇒ 新增判别型下界而**未实测登记** ⇒ 红；台账里有而代码里没了 ⇒ 红。
//  2. **棘轮**：每处的当前阈值必须 ≤ 实测自然值最小值的 **90%**
//     ⇒ 有人把下界改大 ⇒ 红。
//  3. **台账不许是空壳**：每条都必须有实测样本数与实测值 ⇒ 红。
//
// ★ 诚实边界（必须写在这里，否则下一个人会过度相信这道门）：
//   · 自然值会随实现变化漂移，台账是**某一时点**的快照
//     ⇒ 门抓得到「下界被改大」，**抓不到「自然值被改小」**；
//   · 所以「新增判别型下界」被登记册逼着先实测，这是它真正的价值；
//   · 上界（兜底型 42 处）**不在**本门范围内 —— 它们的判据是「阈值 ≫ 自然值」
//     这一**结构性判断**，不是实测。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// measuredMarginEntry is one row of measured_margins.tsv.
type measuredMarginEntry struct {
	File         string
	Func         string
	MeasuredMin  time.Duration
	MeasuredMax  time.Duration
	Samples      int
	SafetySource string
}

// marginLedgerFile is the data file, relative to this package's directory.
const marginLedgerFile = "measured_margins.tsv"

// marginAllowedRatio is the ratchet: the current lower bound must not exceed
// this fraction of the measured **worst** (minimum) natural value.
//
// 90% ⇒ 至少留 10% 的余量。实测 8 处里最紧的一处（governor_debt）是
// 25ms / 30.03ms = 83.3%，仍在界内；再往上收就会把**当前正确**的判据判红。
const marginAllowedNumerator = 9
const marginAllowedDenominator = 10

// violatesMargin is the ratchet rule, extracted as a pure function so it can
// be tested on its own (a rule that only exists inline in a test body is a
// rule nobody can prove has teeth).
func violatesMargin(bound, measuredMin time.Duration) bool {
	if measuredMin <= 0 {
		// No measurement ⇒ cannot claim a margin. Fail closed.
		return true
	}
	return bound*time.Duration(marginAllowedDenominator) >
		measuredMin*time.Duration(marginAllowedNumerator)
}

func loadMarginLedger(t *testing.T, path string) []measuredMarginEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger %s: %v", path, err)
	}
	var out []measuredMarginEntry
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 6 {
			t.Fatalf("ledger %s:%d has %d columns, want 6: %q", path, i+1, len(cols), line)
		}
		minNS, err := strconv.ParseInt(cols[2], 10, 64)
		if err != nil {
			t.Fatalf("ledger %s:%d bad measured_min_ns %q: %v", path, i+1, cols[2], err)
		}
		maxNS, err := strconv.ParseInt(cols[3], 10, 64)
		if err != nil {
			t.Fatalf("ledger %s:%d bad measured_max_ns %q: %v", path, i+1, cols[3], err)
		}
		samples, err := strconv.Atoi(cols[4])
		if err != nil {
			t.Fatalf("ledger %s:%d bad samples %q: %v", path, i+1, cols[4], err)
		}
		out = append(out, measuredMarginEntry{
			File:         cols[0],
			Func:         cols[1],
			MeasuredMin:  time.Duration(minNS),
			MeasuredMax:  time.Duration(maxNS),
			Samples:      samples,
			SafetySource: cols[5],
		})
	}
	return out
}

func ledgerPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "internal", "clockguard", marginLedgerFile)
}

// TestMarginRuleHasTeeth is the **self-check** of the ratchet rule.
//
// A rule that is only ever exercised by "the repo currently satisfies it"
// proves nothing — the same lesson as "门绿 ≠ 量具有牙". This test pins the
// rule's behaviour on both sides of the line, with synthetic values.
func TestMarginRuleHasTeeth(t *testing.T) {
	const ms = time.Millisecond
	for _, tc := range []struct {
		name        string
		bound       time.Duration
		measuredMin time.Duration
		wantBad     bool
	}{
		{"宽裕（余量 50%）", 50 * ms, 100 * ms, false},
		{"刚好在界内（90%）", 90 * ms, 100 * ms, false},
		{"刚好越界（90.1%）", 901 * ms, 1000 * ms, true},
		{"余量为零（distlock 同型）", 200 * ms, 200 * ms, true},
		{"下界大于自然值", 300 * ms, 200 * ms, true},
		{"★没有实测数据 ⇒ 判红（fail closed）", 10 * ms, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := violatesMargin(tc.bound, tc.measuredMin)
			if got != tc.wantBad {
				t.Fatalf("violatesMargin(%v, %v) = %v, want %v", tc.bound, tc.measuredMin, got, tc.wantBad)
			}
		})
	}
}

// TestDiscriminativeLowerBoundsAreAllMeasured is the gate.
//
// Every clock assertion that judges "it must not have been too fast" must be
// in the ledger **with a measurement**, and its current threshold must still
// leave the measured margin intact.
//
// The equality check is bidirectional on purpose:
//   - a site in the code but not in the ledger ⇒ someone added a new
//     discriminative lower bound without measuring its natural value
//     (the R66 hole, re-opened);
//   - a site in the ledger but not in the code ⇒ the ledger drifted.
func TestDiscriminativeLowerBoundsAreAllMeasured(t *testing.T) {
	root := repoRoot(t)
	scanned, err := FindDiscriminativeLowerBounds(root, filepath.Join("internal", "clockguard"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	ledger := loadMarginLedger(t, ledgerPath(t))

	// ── 自检：扫描器不许「扫了个寂寞」 ──────────────────────────────────
	// The inventory this gate is built on is 8 product sites. If the scan
	// suddenly returns far fewer, the gate would be comparing against a
	// truncated view and could report "all good" while missing sites.
	if len(scanned) < 8 {
		t.Fatalf("scan found only %d discriminative lower bounds, want >= 8 — "+
			"the scan lost coverage, so a green gate here would prove nothing", len(scanned))
	}
	if len(ledger) < 8 {
		// ★ 用 Errorf 而不是 Fatalf：这一条自检**不许掩盖**下面的精确诊断。
		//   （M2 变异实测：早先用 Fatalf 时，删掉台账一行只报出「台账条目数 < 8」，
		//   真正该说的「代码里有、台账里没有」反而没被打印 ⇒ 定位成本变高。）
		//   扫描条目数那条仍留 Fatalf：没有扫描结果时，后面的对账无从谈起。
		t.Errorf("ledger holds %d entries, want >= 8 — the ledger lost rows "+
			"(下面会逐条指出是哪一条)", len(ledger))
	}

	// ── ① 登记册严格相等（双向）────────────────────────────────────────
	inCode := map[string]DiscriminativeLowerBound{}
	for _, b := range scanned {
		inCode[b.File+"|"+b.Func] = b
	}
	inLedger := map[string]measuredMarginEntry{}
	for _, e := range ledger {
		key := e.File + "|" + e.Func
		if _, dup := inLedger[key]; dup {
			t.Errorf("ledger has a duplicate entry for %s", key)
		}
		inLedger[key] = e
	}
	for key, b := range inCode {
		if _, ok := inLedger[key]; !ok {
			t.Errorf("判别型下界 %s:%d (%s) 不在实测台账里 ⇒ 它的自然值**从未被实测**。\n"+
				"  修法：用探针复制该测试的等待序列，量 10~30 次自然值，把结果登记进\n"+
				"  internal/clockguard/measured_margins.tsv（格式见文件头），再提交。\n"+
				"  ⇒ 为什么必须先测：余量按构造接近零时（admin/distlock 的 0.03ms），\n"+
				"  跑再多次也不会红 ⇒ 「包级全绿」证明不了余量。",
				b.File, b.Line, b.Func)
		}
	}
	for key, e := range inLedger {
		if _, ok := inCode[key]; !ok {
			t.Errorf("实测台账里的 %s (%s) 在代码里找不到对应的判别型下界 ⇒ 台账漂移 "+
				"（要么判据被删了，要么改了函数名）", key, e.Func)
		}
	}

	// ── ② 棘轮：当前阈值必须 ≤ 实测自然值最小值的 90% ────────────────────
	for key, b := range inCode {
		e, ok := inLedger[key]
		if !ok {
			continue // already reported above
		}
		if violatesMargin(b.Bound, e.MeasuredMin) {
			t.Errorf("%s:%d (%s) 的判别型下界 %s 已经贴到实测自然值上：\n"+
				"  当前阈值 = %v，实测自然值最小 = %v（%d 次实测，%s）\n"+
				"  允许上限 = 自然值 × %d/%d = %v\n"+
				"  ⇒ 这是 admin/distlock 那处的**同型病根**（余量按构造接近零 ⇒ 一条等着的假红）。\n"+
				"  修法：下界表达**粗判意图**（「不是瞬间」），判别职责交给行为断言。",
				b.File, b.Line, b.Func, b.BoundText, b.Bound, e.MeasuredMin, e.Samples,
				e.SafetySource, marginAllowedNumerator, marginAllowedDenominator,
				e.MeasuredMin*time.Duration(marginAllowedNumerator)/time.Duration(marginAllowedDenominator))
		}
	}

	// ── ③ 台账不许是空壳 ───────────────────────────────────────────────
	for _, e := range ledger {
		if e.Samples <= 0 {
			t.Errorf("台账 %s (%s) 的样本数 = %d ⇒ 从来没实测过", e.File, e.Func, e.Samples)
		}
		if e.MeasuredMin <= 0 {
			t.Errorf("台账 %s (%s) 的实测自然值 = %v ⇒ 从来没实测过", e.File, e.Func, e.MeasuredMin)
		}
		if e.MeasuredMax < e.MeasuredMin {
			t.Errorf("台账 %s (%s) 的实测区间反了：min=%v > max=%v", e.File, e.Func, e.MeasuredMin, e.MeasuredMax)
		}
		if strings.TrimSpace(e.SafetySource) == "" {
			t.Errorf("台账 %s (%s) 没有写「自然值为什么不会往下漂」⇒ 下一个读台账的人"+
				"无法判断这条判据的安全条件", e.File, e.Func)
		}
	}
}
