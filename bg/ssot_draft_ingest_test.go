package bg

// 「一份真实的 -emit-ssot 草案能不能被权威加载器接受」这条判据。
//
// # 为什么要单独一条（既有判据的覆盖面边界）
//
// `cmd/tools/propose-baseline-prices/draft_ssot_test.go` 的
// `TestDraftIsAcceptedByTheAuthoritativeGate` 已经用 **bg 自己的
// BaselinePrice** 解码工具产出、再用 **bg 自己的 Validate** 逐条过
// —— 形状接缝是被测住的。它用的是 `proposalWith(2)`，也就是**构造出来的**
// 提案，不是真跑 `docs/02-resources/research/pricing/raw/` 得到的产物。
//
// ⇒ 「形状一致」有判据；「**这一份**草案可入库」没有。两者不是同一件事：
// 真跑一遍还会带上 fixture 造不出的东西 —— 真实厂商名、真实 source_url、
// 真实 `fetched_at`，以及解析器在真实页面上才会踩到的取值。
//
// 这条判据把「合入 SSOT」从一个手工步骤变成一个**可跑的门**：
//
//	LLM_GATEWAY_SSOT_DRAFT=/tmp/draft-ssot.json go test ./bg/ -run TestRealSSOTDraft
//
// # 为什么用真加载器而不是 json.Unmarshal
//
// `loadBaselineCatalog` 内部会对每一条调 `p.validate(name)`，而 validate
// **缺 source_url 一律拒绝**（没有出处的价格不可审计）。只做 Unmarshal 的话，
// 一份缺 source_url 的草案会静静通过，那正是「价格漂到没人知道」的第一步。
//
// 依赖：不依赖真库。环境变量未设置时跳过（它是**按需**跑的门，不是常驻门）。

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const ssotDraftEnv = "LLM_GATEWAY_SSOT_DRAFT"

func TestRealSSOTDraftIsAcceptedByTheAuthoritativeLoader(t *testing.T) {
	path := strings.TrimSpace(os.Getenv(ssotDraftEnv))
	if path == "" {
		t.Skipf("%s not set — point it at a `propose-baseline-prices -emit-ssot` artifact "+
			"to check that a real draft is ingestible by the authoritative loader", ssotDraftEnv)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the draft at %s: %v", path, err)
	}

	// 先量具自证：顶层必须真的带 draft=true。
	//
	// 为什么在意这个：loadBaselineCatalog 只读 models、忽略其余顶层键，所以
	// 一份**草案**对加载器来说与权威面**长得一模一样**。draft 标记是文件里
	// 唯一区分二者的东西；它若被去掉，「这是草稿不是权威面」就只剩人的记性。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("the draft is not a JSON object: %v", err)
	}
	flagRaw, ok := top["draft"]
	if !ok {
		t.Fatalf("the draft has no top-level \"draft\" key (keys: %v) — a draft and the "+
			"authoritative catalog parse identically through loadBaselineCatalog, so this flag "+
			"is the only thing telling them apart", sortedTopKeys(top))
	}
	var isDraft bool
	if err := json.Unmarshal(flagRaw, &isDraft); err != nil {
		t.Fatalf("the \"draft\" key is not a boolean: %v", err)
	}
	if !isDraft {
		t.Error("draft=true expected on a -emit-ssot artifact; if this file is meant to BE the " +
			"authoritative catalog then it must have been reviewed and renamed deliberately, " +
			"not emitted")
	}

	// 承重：走**真**加载器（内部逐条 validate）。
	catalog, err := loadBaselineCatalog(raw)
	if err != nil {
		t.Fatalf("the authoritative loader REJECTED the draft: %v — merge it only after fixing "+
			"this; the loader is the same code path SyncBaselinePricesToDB uses, so a draft it "+
			"rejects would be rejected at sync time too", err)
	}
	if len(catalog) == 0 {
		t.Fatal("the draft parsed to ZERO entries — a successful run that yields an empty " +
			"catalog is indistinguishable from a broken run unless someone looks")
	}

	// 逐条再显式过一遍 Validate，并打印出来：合入评审要的是**清单**，
	// 不是一句「14 条都过了」。
	t.Logf("%s: %d entr(ies) accepted by the authoritative loader", path, len(catalog))
	for _, name := range sortedKeys(catalog) {
		p := catalog[name]
		if err := p.Validate(name); err != nil {
			t.Errorf("entry %q fails Validate: %v", name, err)
			continue
		}
		t.Logf("  %-24s %s/%s %s  vendor=%s fetched_at=%s",
			name, fmtPrice(p.InputPer1M), fmtPrice(p.OutputPer1M), p.Currency, p.Vendor, p.FetchedAt)
	}
}

func sortedTopKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]BaselinePrice) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fmtPrice 只为日志可读：`0.30000000000000004` 那种尾巴对「这条值对不对」
// 的判断没有帮助，而打印 0.3 才让人能在这一步就看出值变了。
func fmtPrice(v *float64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
