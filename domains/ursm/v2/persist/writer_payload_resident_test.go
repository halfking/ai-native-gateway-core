// 守卫（2026-10-18 §10.110.62）：**闭合 payload 排除表的第二个方向**。
//
// # 上游那道门为什么不够
//
// TestPayloadDuplicateKeysCoverAllTypedHashKeys 守的是
//
//	writer.go 的 hash["..."] 读取  ⊆  payloadDuplicateKeys
//
// 它的**全集**是 writer.go 自己。于是「store 层读了、而 writer 压根没读」
// 的键对它**结构性不可见** —— 不是清单漂移，是取样面选错了层。
//
// # 生产实测（为什么这不是假想）
//
// 快照表 payload 里带着 8 个键，全部由 store 层在读、却都不是 typed 列：
//
//	empty_response_rate_5m   ← 它的 _1m / _30m 两个兄弟**已经是列**
//	empty_responses_1m/5m/30m
//	lat_p95_ms
//
// 其中 `lat_p95_ms` 更进一步：全仓没有任何地方往 Redis 写它 ⇒ 快照表里
// 该列最新一轮 1816 行全 NULL、最近 200 轮累计 0 次非 NULL。
//
// # 本门钉的契约
//
//	store 的每一个 hash 读取  ⊆  payloadDuplicateKeys ∪ payloadResidentKeys
//
// 即：每个被读的键都必须有**显式归属** —— 要么有 typed 列，要么被登记为
// 「只存在于 payload 的兜底仓」。落进第三种状态（谁都不管）就红。
//
// # ⚠ 取样面必须覆盖**全部访问器形态**
//
// store 读 hash 不止 `raw["x"]` 一种：还有 `boolField(raw, "x")` 这类函数式
// 读取（probe_evidence.go）。只认下标形态会漏掉 `last_request_failed`
// —— 而它恰恰是"有读者、无列"的典型。正则少一种形态 = 全集变小 = 门变松。
package persist

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// 下标形态：raw["x"] / hash["x"] / mr["x"]
	reStoreSubscriptRead = regexp.MustCompile(`(?:raw|hash|mr)\["([a-z0-9_]+)"\]`)
	// 函数形态：boolField(raw, "x") 等一切 <访问器>(raw, "x")
	reStoreFuncRead = regexp.MustCompile(`\w+\(\s*raw\s*,\s*"([a-z0-9_]+)"`)
)

// storeAccessorShapes 是「store 读 hash 的全部形态」的登记表。
//
// ★ 登记表本身要**自净**：下面那条门要求每个形态在真实 store 源码里
//
//	至少命中一次。命中 0 次的形态会被点名删掉 ——
//	否则一个早就没人用的匹配器会静默留在取样逻辑里，
//	让人误以为「payload 也在被 Go 侧读」（实测并没有）。
var storeAccessorShapes = []struct {
	name  string
	re    *regexp.Regexp
	probe string
}{
	{"下标 raw[\"x\"]", reStoreSubscriptRead, `v.X = atoi(raw["zz_subscript_probe"])`},
	{"函数 boolField(raw, \"x\")", reStoreFuncRead, `failed, _ := boolField(raw, "zz_func_probe")`},
}

// storeReadKeys 收集 store 包（非测试）里读到的全部 hash 键。
//
// ⚠ 解析不到任何键时必须报错而不是返回空集 ——
// 「空集 ⊆ 任意集合」恒真，那会让这道门变成摆设。
func storeReadKeys(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "store", "*.go"))
	if err != nil {
		t.Fatalf("glob store: %v", err)
	}
	var parsed int
	keys := map[string]bool{}
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		parsed++
		src := string(b)
		for _, shape := range storeAccessorShapes {
			for _, m := range shape.re.FindAllStringSubmatch(src, -1) {
				keys[m[1]] = true
			}
		}
	}
	if parsed == 0 {
		t.Fatal("no store/*.go files parsed — the path or glob is stale, " +
			"an empty key set would satisfy this gate vacuously")
	}
	if len(keys) == 0 {
		t.Fatal("store read set is empty — the three accessors went stale, " +
			"this gate is currently vacuous")
	}
	return keys
}

func TestStoreReadKeysAreTypedOrDeclaredResident(t *testing.T) {
	reads := storeReadKeys(t)

	typed := map[string]bool{}
	for _, k := range payloadDuplicateKeys {
		typed[k] = true
	}
	resident := map[string]bool{}
	for _, k := range payloadResidentKeys {
		if typed[k] {
			t.Errorf("%q is in BOTH payloadDuplicateKeys and payloadResidentKeys — "+
				"it is claimed to have a typed column and to be payload-only at once", k)
		}
		if resident[k] {
			t.Errorf("payloadResidentKeys contains %q twice", k)
		}
		resident[k] = true
	}

	var undeclared []string
	for k := range reads {
		if !typed[k] && !resident[k] {
			undeclared = append(undeclared, k)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("store reads these hash keys but nothing declares where they live:\n  %v\n"+
			"each one must either become a typed column (add to payloadDuplicateKeys "+
			"with the Row field it maps to) or be registered in payloadResidentKeys "+
			"as payload-only — otherwise it is invisible to both 818 and this gate",
			undeclared)
	}
}

// payloadResidentKeys 里每个键都必须真的被 store 读到。
// 防的是「登记表自己漂移」：登记了一个谁都不读的键，会让人以为它有读者。
func TestPayloadResidentKeysAreActuallyRead(t *testing.T) {
	reads := storeReadKeys(t)
	var stale []string
	for _, k := range payloadResidentKeys {
		if !reads[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("payloadResidentKeys lists keys the store no longer reads: %v\n"+
			"they are neither typed columns nor read residuals — drop them from the list "+
			"(or, if a typed column was added meanwhile, move them to payloadDuplicateKeys)",
			stale)
	}
}

// 门的**量具自检**：把三个访问器形态里任意一个打坏，取样面必须变小。
//
// ⚠ 没有这条，上面所有断言都可能是在一个**被人为缩小**的全集上做的 —
// 而全集变小只会让断言更容易通过，与门变松的方向一致。
func TestStoreReadKeyExtractorActuallySeesEveryAccessorShape(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "store", "*.go"))
	if err != nil {
		t.Fatalf("glob store: %v", err)
	}
	var corpus strings.Builder
	for _, f := range files {
		if strings.HasSuffix(filepath.Base(f), "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		corpus.Write(b)
	}

	for _, shape := range storeAccessorShapes {
		t.Run(shape.name, func(t *testing.T) {
			// (a) 形态本身可被自己的正则命中（正则没写坏）
			if !shape.re.MatchString(shape.probe) {
				t.Fatalf("shape %q is NOT matched by %s — the extractor lost this "+
					"shape, shrinking the sample set and making the gate looser",
					shape.name, shape.re)
			}
			// (b) 这个形态在**真实源码**里仍存在（形态没被淘汰却还登记着）
			if !shape.re.MatchString(corpus.String()) {
				t.Fatalf("shape %q matches 0 lines in store/*.go — it no longer exists, "+
					"so keeping it in storeAccessorShapes only pretends to cover it. "+
					"Delete it from the registry", shape.name)
			}
		})
	}
}
