package bg

// 互证查找的键归一必须在**存与查两侧一致**。
//
// # 缺口（2026-10-06 实测撞出来的）
//
// `FetchMachineReadablePrices` 原来把观察价存成 `perModel[modelID]`（原样，
// 例如 `MiniMax-M2`），而 `LookupObservation` 查的时候做
// `perModel[strings.ToLower(model)]`。⇒ **只归一了查询侧、没归一存储侧**，
// 两者永远对不上 —— 除非那个厂商在观察源里的 id 恰好全是小写。
//
// 代价不是「多查无果」这种无害的 miss，而是**把可互证的价格报成不可互证**：
//
//	提案侧传给 LookupObservation 的是 canonical（小写，如 `minimax-m3`），
//	观察源的键是显示名（`MiniMax-M3`）⇒ 恒 miss ⇒ 判词变成
//	「no observation … single-sourced, not corroborated」。
//
// 而 models.dev 明明有 MiniMax-M3 = 0.3/1.2 USD/1M。⇒ 那是**假阴性**，
//   而它正好打在按 token 加权占 81% 的那个模型上。误报的代价是：一条本来
//   能与独立信源对上的原厂价被按「仅单源」扣下，人去查一个**不存在**的
//   不一致。
//
// 爆炸半径实测（2026-10-06，models.dev 226 厂商 / 7961 个有价模型）：
// **870 个 id 含大写（10.9%），跨 68 个厂商**，含四个 MiniMax provider 键。
// Claude / gpt 系的 id 在观察源里本来就是小写，所以此前一直是对的 ——
// 缺陷只对「id 带大写的厂商」发作，**恰好是最少被测到的那一类**。
//
// # 为什么既有判据抓不到
//
// `bg/pricing_baseline_sync_test.go` 与 `cmd/tools/propose-baseline-prices/
// crosscheck_test.go` 的夹具模型 id 全是小写（`m-pricey` 之类），小写键对
// 原样键与小写查询都成立 ⇒ 缺陷在**每一个**既有夹具下都是隐形的。
//
// ★ 判据的覆盖面要按**被测代码会遇到的输入**量，不是按「现有夹具都覆盖了」
//   量。所以下面用的载荷刻意照抄 models.dev 里 MiniMax 那几个键的**原样
//   大写形状**。
//
// # 为什么跑真函数而不是手搓 observedPrices
//
// 第一版把 observedPrices 手工构造成大写键，结果修好之后判据**反而红** ——
// 因为手工夹具在自测自己，与 fetch 的真实输出脱钩。⇒ 改成喂一份
// 混合大小写 id 的真实形状 JSON 进去，让真的 `FetchMachineReadablePrices`
// 解析并落键，再拿 canonical（小写）去查。这样「解析 → 存储 → 查找」三段
// 由同一个不变量牵着，抽掉存储侧的 `strings.ToLower` 才会红。
//
// 依赖真库：不需要。传输层被替掉（复用 observation_health_empty_catalog_test.go
// 里的 roundTripperFunc），解析、URL、键构造全是真代码。

import (
	"context"
	"net/http"
	"testing"
)

// mixedCaseObservationJSON 照抄 models.dev 里 MiniMax 的**原样键形状**：
// provider 键小写、model id 大写。另加一个 OpenAI 的小写 id 与一个带前缀的
// id，用来防「修法只处理大写」与「兜底被顺手弄坏」。
const mixedCaseObservationJSON = `{
  "minimax": {
    "name": "MiniMax (minimax.io)",
    "models": {
      "MiniMax-M3": {"id": "MiniMax-M3", "cost": {"input": 0.3, "output": 1.2}}
    }
  },
  "minimax-cn": {
    "name": "MiniMax (minimax.cn)",
    "models": {
      "MiniMax-M3": {"id": "MiniMax-M3", "cost": {"input": 0.3, "output": 1.2}}
    }
  },
  "anthropic": {
    "name": "Anthropic",
    "models": {
      "claude-fable-5": {"id": "claude-fable-5", "cost": {"input": 10, "output": 50}}
    }
  },
  "openai": {
    "name": "OpenAI",
    "models": {
      "openai/gpt-4o": {"id": "openai/gpt-4o", "cost": {"input": 2.5, "output": 10}},
      "gpt-4o-mini":   {"id": "gpt-4o-mini",  "cost": {"input": 0.15, "output": 0.6}}
    }
  }
}`

func fetchObservedForTest(t *testing.T) observedPrices {
	t.Helper()
	t.Setenv(machineReadablePricingURLEnv, "https://observation.test/api.json")
	observed, _, _, err := FetchMachineReadablePrices(context.Background(),
		&http.Client{Transport: jsonResponder(mixedCaseObservationJSON, http.StatusOK)})
	if err != nil {
		t.Fatalf("fetch the canned observation source: %v", err)
	}
	return observed
}

// TestObservationLookupFindsMixedCaseIDsByCanonicalName 是主判据：提案侧传进来的
// 是 canonical（小写），观察源里是显示名（大写），两者必须对得上。
func TestObservationLookupFindsMixedCaseIDsByCanonicalName(t *testing.T) {
	observed := fetchObservedForTest(t)

	cases := []struct {
		name, vendor, model string
		wantIn, wantOut     float64
	}{
		// ★ 缺陷的正主：canonical 小写 vs 观察源大写。
		{"小写 canonical 命中大写 id", "minimax", "minimax-m3", 0.3, 1.2},
		{"另一个 MiniMax provider 键同样命中", "minimax-cn", "minimax-m3", 0.3, 1.2},
		// 阳性对照：本来就全小写的 id 不得被归一弄坏。
		{"全小写 id 不受影响", "anthropic", "claude-fable-5", 10, 50},
		{"全小写 id（另一个厂商）不受影响", "openai", "gpt-4o-mini", 0.15, 0.6},
		// 第二个兜底：观察源用带前缀的 id。
		{"带前缀 id 经 vendor 前缀兜底命中", "openai", "gpt-4o", 2.5, 10},
	}
	for _, c := range cases {
		obs, ok := observed.LookupObservation(c.vendor, c.model)
		if !ok {
			t.Errorf("%s: LookupObservation(%q, %q) = not found — the store side and the query "+
				"side must normalise keys the same way, otherwise every vendor whose model id in "+
				"the observation source contains uppercase is silently reported as uncorroborated "+
				"(measured on models.dev 2026-10-06: 870/7961 priced models, 68 providers, all "+
				"four MiniMax provider keys included)", c.name, c.vendor, c.model)
			continue
		}
		if obs.InputPer1M == nil || obs.OutputPer1M == nil {
			t.Errorf("%s: observation found but prices are nil: %+v", c.name, obs)
			continue
		}
		if *obs.InputPer1M != c.wantIn || *obs.OutputPer1M != c.wantOut {
			t.Errorf("%s: got %v/%v, want %v/%v — the lookup found a *different* model than the "+
				"one asked for, which is worse than not finding it",
				c.name, *obs.InputPer1M, *obs.OutputPer1M, c.wantIn, c.wantOut)
		}
	}
}

// TestObservationLookupDoesNotWidenTheVendorMatch 钉住「归一只作用于键，
// 不放宽厂商匹配」：厂商名对不上就是查不到。防止把修法做成「忽略厂商」。
func TestObservationLookupDoesNotWidenTheVendorMatch(t *testing.T) {
	observed := fetchObservedForTest(t)
	if _, ok := observed.LookupObservation("anthropic", "minimax-m3"); ok {
		t.Error("LookupObservation matched model 'minimax-m3' under vendor 'anthropic' — the " +
			"case normalisation must not turn into a vendor-agnostic search; that would let a " +
			"price from a different vendor corroborate this one")
	}
	if _, ok := observed.LookupObservation("", "minimax-m3"); ok {
		t.Error("LookupObservation matched with an empty vendor — an unlabelled price must never " +
			"count as a corroborated original-vendor reference")
	}
}
