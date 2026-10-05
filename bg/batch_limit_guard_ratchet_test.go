package bg

// ratchet：`bg/` 周期任务里的**批次上限守卫**，不允许用裸比较。
//
// # 为什么是 ratchet 而不是又一条逐条判据
//
// 同一个形态已经咬了两次：
//
//  1. `bg/modality_verification.go` 的 `if probed >= m.batchLimit { break }` ——
//     `batchLimit==0` 时首轮即 break，整个核实 worker 静默什么都不做，**不报错**、
//     日志照样打 `cycle done`（只是 `scanned=4 probed=0`）。
//  2. `bg/capability_backfill.go:395` 是一模一样的裸守卫。
//
// 两次都是「我先修了被我看见的那一处」—— 另一处是靠 grep 侥幸撞见的。
// 第三次不能再靠运气：任何**新增**的周期 worker 写裸守卫都必须红。
//
// # 规则
//
// 任何形如
//
//	if <probed> >= <recv>.batchLimit {
//
// 的守卫，同一行里必须带 `batchLimit > 0 &&`。也就是：
//
// **非正的批次上限必须读作「未设置」而不是「一条都不做」。**
//
// 这与 `budgetRemaining` 那个约定是同一条纪律的两面：
// 「关闭某个限制」不等于「不检查它」，把上限配成 0 不该静默变成「完全停摆」。
//
// 豁免走 `batchLimitGuardAllowlist`，且必须写理由 —— 加进豁免表和直接写裸守卫
// 一样要过评审，但至少留下一行可 grep 的说明。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// batchLimitGuardRE 匹配裸的批次上限守卫。
//
// 只认 `>= ….batchLimit {` 这一种形状：带 `> 0 &&` 的写法因为 `&&` 在中间，
// 不会被这条正则匹配到（见测试末尾的自证）。
var batchLimitGuardRE = regexp.MustCompile(`if\s+(\w+)\s*>=\s*(\w+)\.batchLimit\s*\{`)

// batchLimitGuardAllowlist 是「这一处不是周期任务的批次上限」的具名豁免。
//
// 刻意留空结构而不是空切片：将来真的需要豁免时，加一行 + 一句理由，
// 就在 `git blame` 里看得见。空豁免表时下面会断言它确实是空的。
var batchLimitGuardAllowlist = map[string]string{}

func TestNoPeriodicWorkerBreaksOnANonPositiveBatchLimit(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob bg/*.go: %v", err)
	}

	// 自证：这条正则必须**只**匹配裸守卫。若它连带 `&&` 的合规写法也匹配，
	// 下面每一条报告都是假的（而判据会一直红，红得毫无信息量）。
	if batchLimitGuardRE.MatchString("if probed >= m.batchLimit > 0 &&") {
		t.Fatal("batchLimitGuardRE matches a guarded comparison; the scanner is measuring " +
			"something other than what it claims to")
	}
	if !batchLimitGuardRE.MatchString("if probed >= m.batchLimit {") {
		t.Fatal("batchLimitGuardRE does not match the bare guard shape it is supposed to find")
	}

	var found []string
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			// 注释行不算代码：它们描述的往往正是那个**尚未**修的形态。
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !batchLimitGuardRE.MatchString(line) {
				continue
			}
			site := name + ":" + strconv.Itoa(i+1)
			if reason, ok := batchLimitGuardAllowlist[site]; ok {
				_ = reason // 豁免条目本身在评审里可见
				continue
			}
			if strings.Contains(line, "batchLimit > 0") {
				continue
			}
			found = append(found, site+": "+strings.TrimSpace(line))
		}
	}

	for _, f := range found {
		t.Errorf("%s — a non-positive batchLimit reads as \"do nothing\" here, so the worker stops "+
			"silently: no error, and the cycle log still says \"cycle done\". Write it as "+
			"`if <recv>.batchLimit > 0 && <probed> >= <recv>.batchLimit` so that unset means unlimited",
			f)
	}

	// 每条豁免都必须带理由。⚠ 这里**不**检查豁免条目是否已过期（对应代码被修好后
	// 条目会变成永不命中的死条目）—— 那个检查需要行号到内容的反向索引，写出来
	// 只会是一段看起来在检查、其实什么都不断言的代码。改用 grep 清理：
	//   grep -n '<行号>' bg/<file>.go
	for site, reason := range batchLimitGuardAllowlist {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("allowlist entry %s has no reason; an unexplained exemption is worse than "+
				"the bare guard, because it reads as 「this one was thought about」", site)
		}
	}
}

// TestBatchLimitZeroFallsBackToTheDocumentedDefault 钉住这条规则的**行为**面。
//
// ratchet 扫的是**形状**（有没有 `> 0`），但那条形状不保证语义：把守卫改成
// `if probed >= 0 { break }` 也是「带 0 判断」的一种，而它做的正是我们刚修掉的事。
// 所以两个 worker 各有一条行为断言，都不需要数据库 ⇒ 永远会跑。
//
// 承重是**有界**：未设置必须回落到**包常量**，而不是「不限」。
// 探针是花钱的，把上限配成 0 换来的不该是「无上限出网」。
func TestBatchLimitZeroFallsBackToTheDocumentedDefault(t *testing.T) {
	t.Run("capability_backfill", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			batchLimit int
			want       int
		}{
			{"configured", 7, 7},
			{"zero means unset", 0, capabilityBackfillBatchLimit},
			{"negative means unset", -3, capabilityBackfillBatchLimit},
		} {
			b := &CapabilityBackfill{batchLimit: tc.batchLimit}
			if got := b.effectiveBatchLimit(); got != tc.want {
				t.Errorf("%s: effectiveBatchLimit() = %d, want %d", tc.name, got, tc.want)
			}
			// scanLimit 的结果直接进 `LIMIT $3`，所以 0 在那里是「扫不出任何行」
			// 而不是「不限」。这条一并钉住。
			if got := b.scanLimit(); got <= 0 {
				t.Errorf("%s: scanLimit() = %d, want a positive window — it is bound to SQL "+
					"LIMIT, where 0 means \"scan nothing\"", tc.name, got)
			}
		}
		// 扫描窗口必须比每轮上限宽（反饥饿：退避跳过的行后面还有行可取）。
		b := &CapabilityBackfill{batchLimit: 0}
		if b.scanLimit() <= b.effectiveBatchLimit() {
			t.Errorf("scanLimit() = %d must exceed effectiveBatchLimit() = %d — a window equal to "+
				"the per-round cap cannot backfill past rows skipped by the attempt ledger",
				b.scanLimit(), b.effectiveBatchLimit())
		}
	})

	t.Run("modality_verification", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			batchLimit int
			want       int
		}{
			{"configured", 9, 9},
			{"zero means unset", 0, modalityVerifyBatchLimit},
			{"negative means unset", -1, modalityVerifyBatchLimit},
		} {
			m := &ModalityVerification{batchLimit: tc.batchLimit}
			if got := m.effectiveBatchLimit(); got != tc.want {
				t.Errorf("%s: effectiveBatchLimit() = %d, want %d", tc.name, got, tc.want)
			}
			// scanLimit 的结果直接进 dueTargets 的 `LIMIT $2`，那里 0 = 零行。
			scan := m.scanLimit()
			if scan <= 0 {
				t.Errorf("%s: scanLimit() = %d, want a positive window — it is bound to SQL "+
					"LIMIT, where 0 means \"scan zero rows\"", tc.name, scan)
			}
			if scan <= m.effectiveBatchLimit() {
				t.Errorf("%s: scanLimit() = %d must exceed effectiveBatchLimit() = %d — a window "+
					"equal to the per-round cap cannot backfill past rows skipped by the attempt "+
					"ledger", tc.name, scan, m.effectiveBatchLimit())
			}
		}
		// nil 接收者：有些调用点在 worker 尚未接线时可能拿到 nil
		// （VerifyOnce 开头就有 `if m == nil { return 0, nil }`）。
		var nilWorker *ModalityVerification
		if got := nilWorker.effectiveBatchLimit(); got != modalityVerifyBatchLimit {
			t.Errorf("nil receiver: effectiveBatchLimit() = %d, want the documented default %d",
				got, modalityVerifyBatchLimit)
		}
	})
}

// sqlLimitBatchLimitRE 匹配「把某个 worker 的 batchLimit 直接乘进 SQL LIMIT」。
var sqlLimitBatchLimitRE = regexp.MustCompile(`\b\w+\.batchLimit\s*\*\s*\w+`)

// TestSqlLimitIsNotBoundToARawBatchLimit 钉住上一轮那条教训的**应用面**。
//
// # 这条是同一个洞的第二次出现
//
// 上一轮发现：只修**循环守卫**不够，`scanLimit()` 那种喂给 `LIMIT $n` 的函数
// 才是真正的限流器（`LIMIT 0` = 扫不出任何行）。我在 `capability_backfill.go`
// 应用了这条，并把它写进了 changelog —— **却在同一个文件里 300 行外的
// `modality_verification.go` 忘了应用**：`dueTargets` 仍直接用
// `m.batchLimit*modalityVerifyScanFactor`。
//
// `batchLimit==0` ⇒ `LIMIT 0` ⇒ `dueTargets` 返回零行 ⇒ 整个核实 worker 不做事。
// 而上一轮那条行为判据是绿的，因为它用 `scan` 接缝**绕过了真正的 SQL** ——
// 又一次「判据量的是我给的形状，不是真在跑的那条路径」。
//
// # 规则
//
// 任何喂给 SQL `LIMIT` 的 batchLimit 必须经由 `effectiveBatchLimit()`。
var allowRawBatchLimitInSQL = map[string]string{}

func TestSqlLimitIsNotBoundToARawBatchLimit(t *testing.T) {
	// 自证：正则必须匹配目标写法、且**不**匹配已修好的那种。
	if !sqlLimitBatchLimitRE.MatchString("m.batchLimit*modalityVerifyScanFactor") {
		t.Fatal("sqlLimitBatchLimitRE does not match the shape it is supposed to find")
	}
	if sqlLimitBatchLimitRE.MatchString("m.effectiveBatchLimit()*modalityVerifyScanFactor") {
		t.Fatal("sqlLimitBatchLimitRE also matches the compliant shape; every report below " +
			"would be false")
	}

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob bg/*.go: %v", err)
	}
	var found []string
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !sqlLimitBatchLimitRE.MatchString(line) {
				continue
			}
			site := name + ":" + strconv.Itoa(i+1)
			if reason, ok := allowRawBatchLimitInSQL[site]; ok && strings.TrimSpace(reason) != "" {
				continue
			}
			found = append(found, site+": "+strings.TrimSpace(line))
		}
	}
	for _, f := range found {
		t.Errorf("%s — a raw batchLimit is bound into a SQL LIMIT. With batchLimit<=0 that "+
			"becomes LIMIT 0, which scans **zero rows**: the worker then reports \"cycle done\" "+
			"having done nothing, and the failure is invisible in the logs. Bind it through "+
			"effectiveBatchLimit() so that unset falls back to the documented default", f)
	}
}

// batchFieldInLimitRE 匹配「一个批次/上限类字段被直接绑进 SQL LIMIT」。
//
// 上一条 ratchet 只认 `batchLimit` 这个**具体字段名**，而同一个缺陷类会以
// `w.batchSize` / `w.cleanupBatchSize` / `p.cfg.MaxPerTick` 的形态出现 —
// 新增 worker 照着邻近文件的写法抄，很容易抄到裸的那份。
var batchFieldInLimitRE = regexp.MustCompile(`LIMIT \$\d+`)

// batchFieldArgRE 在 LIMIT 之后 10 行内找批次/上限类的**裸字段**实参。
var batchFieldArgRE = regexp.MustCompile(
	`\b\w+\.(batch\w*|\w*[Bb]atchSize|cleanupBatchSize|MaxPerTick|\w*[Ll]imit\w*|topN|overflow)\b`)

// bareBatchFieldRE 过滤掉**方法调用**：`b.scanLimit()` 是合规的归一入口，
// `w.batchSize` 才是裸字段。RE2 没有负向断言，所以在代码里看匹配后紧跟的字符。
//
// ★ 这个区分是自证 #2 逼出来的：第一版正则只写 `\w*[Ll]imit\w*`，于是
// `b.scanLimit()` 也被匹配 —— ratchet 会把**已修好的**两处也报成缺陷。
// 一条永远误报的 ratchet 比没有 ratchet 更糟（人会学会忽略它）。
func bareBatchFields(line string) []string {
	var out []string
	for _, loc := range batchFieldArgRE.FindAllStringIndex(line, -1) {
		if loc[1] < len(line) && line[loc[1]] == '(' {
			continue // 方法调用（归一入口），不是裸字段
		}
		out = append(out, line[loc[0]:loc[1]])
	}
	return out
}

// batchFieldExemption 是「这一处的零值已被别处归一化」的豁免，**带谓词**。
//
// # 为什么必须带谓词（2026-10-05 实测踩到）
//
// 第一版豁免只是 `map[string]string`：站点在表里就不报。它立刻变成一个洞 ——
// 我把 `m.scanLimit()` 回退成 `m.batchLimit*factor`（即**撤销本次修复**），
// ratchet **一声不吭**，因为那个站点还在豁免表里。
//
// ⇒ 豁免的正确形态是「因为它走了 X 所以没事」，而 ratchet 必须**验证 X 还在**。
// 谓词不匹配 ⇒ 豁免失效 ⇒ 照常报告。这让死掉的豁免变成一声响，而不是一片沉默。
//
// 每条还要给出一句可 grep 核实的理由（`reason`），读者不必相信判据。
type batchFieldExemption struct {
	reason string
	// mustMatch 是该站点的实参行必须匹配的正则。取 nil 表示「无条件豁免」，
	// 而那只有在理由是「包常量、非零」这类**不依赖代码形状**的断言时才合法。
	mustMatch *regexp.Regexp
}

var allowBatchFieldInSQL = map[string]batchFieldExemption{
	"capability_backfill.go:639": {
		reason: "b.scanLimit() → effectiveBatchLimit()（见同文件 :573）",
		// 撤销修复（直接用 b.batchLimit）会让这条不匹配 ⇒ 豁免自动失效。
		mustMatch: regexp.MustCompile(`b\.scanLimit\(\)`),
	},
	"modality_verification.go:587": {
		reason:    "m.scanLimit() → effectiveBatchLimit()（见同文件 :858）",
		mustMatch: regexp.MustCompile(`m\.scanLimit\(\)`),
	},
	"integrity_probe_planner.go:195": {
		reason:    "p.cfg.MaxPerTick 在构造函数里归一（:81 `if cfg.MaxPerTick <= 0`）",
		mustMatch: regexp.MustCompile(`p\.cfg\.MaxPerTick`),
	},
	"model_availability_backfill.go:177": {
		reason:    "w.batchSize ← cfg.BatchSize，在构造函数里归一（:61 `if cfg.BatchSize <= 0`）",
		mustMatch: regexp.MustCompile(`w\.batchSize`),
	},
	"credential_autoheal.go:221": {
		// 包常量 autoHealBatchSize 编译期非零 ⇒ 形状无关，无谓词。
		reason: "w.batchSize ← 包常量 autoHealBatchSize（非零，与代码形状无关）",
	},
	"session_lifecycle_worker.go:256": {
		reason:    "w.cleanupBatchSize 默认 500（:148）；WithRecycleConfig 是 test-only 且刻意允许覆盖",
		mustMatch: regexp.MustCompile(`w\.cleanupBatchSize`),
	},
	"session_lifecycle_worker.go:312": {
		reason:    "w.cleanupBatchSize 默认 500（:148）；同上",
		mustMatch: regexp.MustCompile(`w\.cleanupBatchSize`),
	},
}

func TestSqlLimitBatchFieldsAreNormalisedSomewhere(t *testing.T) {
	// 自证一：必须能识别「裸批次字段」。
	if got := bareBatchFields("`, intervalSeconds(w.idleTimeout), w.cleanupBatchSize)"); len(got) != 1 {
		t.Fatalf("bareBatchFields found %v in a line that passes w.cleanupBatchSize raw, want exactly 1", got)
	}
	// 自证二：**不得**把经由归一入口（方法调用）的那类算进去 —— 否则每条报告都是假的。
	if got := bareBatchFields("`, int(b.staleAfter.Seconds()), b.scanLimit())"); len(got) != 0 {
		t.Fatalf("bareBatchFields(%q) = %v, want none: a method call is the compliant shape",
			"b.scanLimit()", got)
	}

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob bg/*.go: %v", err)
	}
	var reported []string
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			// 注释里也会出现 "LIMIT $3"（我自己在解释为什么它危险时写的），
			// 而那不是一条真的 SQL。★ 漏掉这一条时 ratchet 会把**我自己的注释**
			// 报成缺陷 —— 判据红的理由指向了一个不存在的缺陷。
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !batchFieldInLimitRE.MatchString(line) {
				continue
			}
			// 往后 10 行是这条 SQL 的实参列表（多行 raw string 之后接 `, args...)`）。
			for j := i + 1; j < i+11 && j < len(lines); j++ {
				argLine := lines[j]
				if strings.HasPrefix(strings.TrimSpace(argLine), "//") {
					continue
				}
				if len(bareBatchFields(argLine)) == 0 {
					continue
				}
				site := name + ":" + strconv.Itoa(i+1)
				if ex, ok := allowBatchFieldInSQL[site]; ok {
					// 豁免只在**理由仍然成立**时有效：谓词必须匹配这一行的实参。
					if ex.mustMatch == nil || ex.mustMatch.MatchString(argLine) {
						break
					}
					reported = append(reported, fmt.Sprintf(
						"%s — its exemption claims %q, but the site no longer matches that "+
							"claim (arg on :%d: %s). The exemption is STALE: the zero value is "+
							"no longer normalised anywhere, so LIMIT 0 would scan zero rows. "+
							"Fix the site or update the exemption to match reality",
						site, ex.reason, j+1, strings.TrimSpace(argLine)))
					break
				}
				reported = append(reported, fmt.Sprintf("%s (arg on :%d: %s)",
					site, j+1, strings.TrimSpace(argLine)))
				break
			}
		}
	}
	for _, r := range reported {
		t.Errorf("%s — a batch/limit field is bound into a SQL LIMIT with no evidence that its "+
			"zero value is normalised anywhere. LIMIT 0 scans **zero rows**, so an unset field "+
			"silently turns the worker into a no-op that still logs \"cycle done\". Normalise it "+
			"at config load, route it through effective*(), or add an allowlist entry that cites "+
			"the normalising line", r)
	}
}
