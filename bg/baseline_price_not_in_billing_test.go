package bg

// baseline_price_not_in_billing_test.go —— 「基准价只做合理性下限告警，不参与金额计算」
//
// 这条判据是 2026-10-07 人工拍板（定价决策，非工程可推导）的机器化表达。
//
// ── 拍板内容 ──────────────────────────────────────────────────────────
// 提问：在成本核算里，原厂基准价（models_canonical 的 baseline_*_price_per_1m）
//       应当扮演什么角色？计费取的是 Candidate.PriceInPer1M（供应商实际报价），
//       baseline_* 目前零计费消费者，接线前需要先定调。
// 答：  基准价只做「合理性下限」告警，**不参与金额计算**。
//       （同轮附带口径：将来若接线，缓存写入价按 Anthropic 最短档计，不做 TTL 分档。
//        本判据不阻止那条路，只要求它经过显式改判据 + 显式决策。）
//
// ── 为什么这件事需要一条判据，而不是一句注释 ──────────────────────────
// 「接线」这件事的**成本不对称**：
//   · 不接线的代价：告警照常（supplier_price_drift 已在用 baseline，阈值 1.5x），
//     账目口径干净，没有可见损失。
//   · 接线的代价：**静默地改掉已记账金额的语义**。CalcCost 落库的是 cost_usd，
//     一旦把 baseline 混进去，历史行与新行口径不同，recorded_cost_is_negative
//     这类检查还会在负成本时报警 —— 而根因不是数据错，是口径中途改过。
//
// ⇒ 「不改」看起来是零成本的选择，实际上它是**默认值**。Go 里加一个
//   `if baseline != nil { priceIn = min(priceIn, *baseline) }` 只需要三行，
//   没有任何现有判据会变红（没有任何一条判据主张「baseline 不得进金额」）。
//   ⇒ 本判据的作用是**把默认值改成需要显式推翻的东西**。
//
// ── 判据的形状（双向，都必须有牙）──
//   1. CalcCost 及其调用链不得读 baseline_* 任何一列；
//   2. models_canonical 的 baseline_* 列不得出现在 usage_ledger / request_logs
//      的任何写入语句里（防「不经过 CalcCost 直接落库」这条旁路）。
// 第 2 条不可省：只钉第 1 条的话，在 handler 里先算出 cost 再乘一个 baseline
// 系数落库，CalcCost 源码依然干净。
//
// 判据是**纯静态**的：每次都跑，不需要数据库。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// billingSourceRoots 是「金额可能从模型基准价里取值」的源码面。
//
// 只扫 domains/ 与 bg/：baseline_* 的写入方 bg/pricing_baseline_sync.go 在
// **允许**的范围内（它写的是 models_canonical 那一族列，不是账单金额），
// 所以下面按文件逐个豁免，而不是按目录排除。
var billingSourceRoots = []string{"domains", "bg"}

// baselineWriteAllowedFiles 是「读得见 baseline_* 字符串」但**不算违规**的文件。
//
// 逐个列出并写明理由，而不是用「不含 test.go 就跳过」这种粗规则 ——
// 因为违规者最可能就藏在一个看起来无辜的生产文件里。
var baselineWriteAllowedFiles = map[string]string{
	// 官方写价入口：它就是 baseline_* 的唯一写入方，必须能读这些列名。
	"bg/pricing_baseline_sync.go": "官方写价入口（models_canonical 的 baseline 族唯一写入方）",
	// 告警 SQL：supplier_price_drift / baseline_price_missing 按拍板结论
	// **就是要**读 baseline 做合理性下限判断。这是 baseline 的**目标用途**。
	"bg/routing_health_checks.go": "合理性下限告警本身（拍板指定的用途）",
	// 826 建视图时的幂等守卫：ADD COLUMN IF NOT EXISTS + CHECK。
	// 注意：它是 .sql 不在本次扫描面内，这里登记只为对称性留痕。
	//
	// ★ 这里**不再**登记判据自身：测试文件已由上面的 `_test.go` 规则整体
	// 排除。留着这条会让「把真逻辑藏进本判据文件」成为一个不红的死角。
}

func TestBaselinePriceNeverReachesBillingAmount(t *testing.T) {
	root := repoRootFromBg(t)

	// baseline_ 列名族。凡是「计算账单金额」的代码看到它，就是接线了。
	// 判据 1：CalcCost 本体 + 它的输入结构里不得出现这些列。
	baselineColumnNeedle := "baseline_input_price_per_1m"

	// 账单落库面：这两个面是「金额真正变成数字」的地方。
	billingSinkNeedles := []string{
		"cost_usd",
		"cost_cents",
		"total_cost",
	}

	for _, dir := range billingSourceRoots {
		dirPath := filepath.Join(root, dir)
		if _, err := os.Stat(dirPath); err != nil {
			continue
		}
		err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "vendor" || strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// ★ 测试文件不进扫描面。
			//
			// 这不是「让判据好过」的豁免，是被测对象的定义：判据问的是
			//「**生产代码**会不会把基准价灌进账单」。测试文件里出现
			// baseline_* 与 cost_usd 是正常的（夹具、断言、错误消息模板
			// 都要写这两个词），把它算进去会让判据**第一跑就红**——
			// 第一版正是这样红的：判据自己的错误消息模板同时含两类字符串，
			// 于是它把自己当成了违规代码。症状看着像「判据写错了」，
			// 实际是扫描面定义错了。
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			if _, allowed := baselineWriteAllowedFiles[filepath.ToSlash(rel)]; allowed {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			body := string(src)
			if !strings.Contains(body, baselineColumnNeedle) {
				return nil
			}
			// 该文件提到了 baseline 列名。它是不是在把基准价灌进账单？
			// 判据：只有当**同一个文件**同时提到账单落库面时才报。
			//
			// ⚠ 这个「同时出现」的门槛是**有意的窄口径**，它只抓
			// 「读基准价 + 算金额」写在同一个文件里的形态。跨文件的形态
			// （A 文件算出 ratio、B 文件落库）抓不到 —— 那属于需要新增
			// 显式接线时的独立复核，不由本条兜底。本条的职责是让
			// 「顺手在 CostInput 旁边加一行 min()」这种最常见、最像无意的
			// 形态变红。
			for _, sink := range billingSinkNeedles {
				if strings.Contains(body, sink) {
					t.Errorf("%s 同时出现基准价列 %q 与账单落库面 %q。\n"+
						"    2026-10-07 人工拍板：基准价只做「合理性下限」告警，不参与金额计算。\n"+
						"    算账单金额时只能用供应商实际报价（Candidate.PriceInPer1M 等）。\n"+
						"    若确要改这条决定，请先改本判据并在 db-changelog 记明决策依据 —— \n"+
						"    代价是已记账的 cost_usd 口径与历史行不再同源。",
						rel, baselineColumnNeedle, sink)
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

// TestBillingAmountSourcesAreSupplierPrices 是判据 1 的正向表达：
// CalcCost 的价格输入必须来自供应商报价，而不是原厂基准价。
//
// 单独写成一条（而不是并入上面那条）是因为它的失效方式不同：
// 上面的判据靠「字符串同时出现」发现问题，这条靠「结构里没有该字段」
// 证明 —— 后者在有人把基准价作为**新字段**接进 CostInput 时会先变红。
func TestBillingAmountSourcesAreSupplierPrices(t *testing.T) {
	root := repoRootFromBg(t)
	usageGo := filepath.Join(root, "domains", "streaming", "usage.go")
	src, err := os.ReadFile(usageGo)
	if err != nil {
		t.Skipf("读不到 %s，本条判据无法成立: %v", usageGo, err)
	}
	body := string(src)

	if strings.Contains(body, "baseline_") {
		t.Errorf("domains/streaming/usage.go 出现了 baseline_ 字样。\n" +
			"    CalcCost 是账单金额的唯一计算入口；按 2026-10-07 拍板，\n" +
			"    它只接受供应商实际报价（PriceIn / PriceOut / CacheReadPrice / CacheWritePrice）。")
	}
}
