// Package hygiene - D17 代码卫生：平台设置 spec 的「有声明必有消费方」普查。
//
// # 这道门在防什么
//
// 死代码不承诺任何东西，运维不会去指望它。
// **死配置会承诺**：它有 Key、有类型、有默认值、有界面上的名称与说明、
// `HotReload: true`、还有 `DangerLevel: Warning`。运维改它、看到保存成功、
// 在界面上看到新值——然后什么也不会发生，且**没有任何报错**。
//
// 2026-09-29 的普查（297 个平台设置 Key）发现 31 个这种设置，其中一组特别整齐：
//
//	lifecycle.usage_ledger_ttl_days          → usage_ledger          1001 MB / 2,038,536 行
//	lifecycle.request_wal_ttl_days           → request_wal            375 MB /   975,153 行
//	lifecycle.credential_model_call_history_ttl_days → …_call_history 102 MB /   284,877 行
//	lifecycle.credit_ledger_ttl_days          → credit_ledger          （尚空）
//	lifecycle.tool_usage_stats_ttl_days      → tool_usage_stats       （尚空）
//
// 这 5 个都被迁移 391 播种进设置表（`('lifecycle.…_ttl_days', '1'::jsonb, …)`），
// 于是**值可存、界面可见、默认 1 天**，而全仓无人读取。
// 而 `drop_old_state_partitions()` 覆盖的恰好是另外 5 张表
// （candidate_failure_logs / credential_model_index / handoff_logs /
// model_probe_runs / routing_decision_log）——**一张都不在这 5 张里**。
//
// # 「消费方」怎么算：三次自我修正
//
// 这道门的第一版连续给出过三个错误答案，全是**检测器自身的错**，不是代码的错：
//
//  1. 把 `.md` 文档算作消费方 → 死设置数从 31 缩到 4。
//     文档提到一个 Key 不代表代码读它。
//  2. 用 `grep <key>`（未转义的 `.`）当判据 → `handoff.threshold` 匹配上了
//     `handoff_threshold`，把一个真死设置判成活的。
//     **Key 里的 `.` 是正则元字符**。本门用精确子串，不用正则。
//  3. 把迁移里的**种子 INSERT** 当消费方 → `lifecycle.*_ttl_days` 5 个全部「存活」。
//     `INSERT INTO settings … VALUES (key, …)` 是**写入默认值**，不是读取。
//     没有任何代码读它，它就还是死的。
//
// 三条合起来是一条通用纪律：**普查结果先拿已知案例校准，再信它。**
// 这与本会话在 DB 侧遇到的三次「判错对象」同源——
// 判据的对象不是你以为的那个对象时，输出会整齐地错，而且错得很自信。
//
// 跑测（纯静态，无需数据库）：
//
//	go test -timeout 120s ./tests/48h-audit/D17-code-hygiene/hygiene/... -count=1
package hygiene

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// specKeyPattern matches `Key: "…"` in a settings spec.
var specKeyPattern = regexp.MustCompile(`Key:\s*"([^"]+)"`)

// seedValueLine recognises a settings-table seed row, i.e. a line inside an
// INSERT … VALUES list. A write of the default value is not a read of it.
//
// The shape targeted is the one migration 391 actually uses:
//
//	('lifecycle.request_wal_ttl_days', '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
//
// A line qualifies when it both mentions the key and looks like a tuple member
// rather than code: a quoted key at the start, more comma-separated fields
// after it, and a type/jsonb marker.
var seedValueLine = regexp.MustCompile(`\(\s*'[^']+'\s*,.*,\s*'(int|bool|string|float|jsonb)'`)

// consumerExtensions are the file kinds that can actually READ a setting.
// Markdown, JSON, and the audit/test trees are excluded on purpose: a key
// mentioned in a document is not wired.
var consumerExtensions = map[string]bool{
	".go": true, ".sql": true, ".vue": true, ".ts": true, ".tsx": true, ".js": true,
}

// skipDirNames are walked past entirely.
var skipDirNames = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, ".build-local": true,
	// Docs and the audit trees describe settings; they never read them.
	// Including them is what shrank this census from 31 to 4 in the first run.
	"docs": true, "tests": true, "scripts": true,
}

type settingKey struct {
	key      string
	declared string // settings/<file>
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// collectDeclaredKeys reads every settings/spec_*.go and returns its Key values.
func collectDeclaredKeys(t *testing.T, root string) []settingKey {
	t.Helper()
	dir := filepath.Join(root, "settings")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read settings dir: %v", err)
	}
	var out []settingKey
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "spec_") || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read settings/%s: %v", name, err)
		}
		for _, m := range specKeyPattern.FindAllStringSubmatch(string(b), -1) {
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, settingKey{key: m[1], declared: "settings/" + name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// repoSourceFile is one file that could read a setting.
type repoSourceFile struct {
	rel  string
	body string
	// seededKeys holds keys that only ever appear inside a settings-table seed
	// tuple, i.e. their default is WRITTEN, never READ.
	seedOnly map[string]bool
}

// loadCandidateFiles walks the repo ONCE. Doing it per key made this gate take
// ~49s (297 keys x ~6.4k files); one pass plus in-memory matching is the same
// answer in well under a second of I/O.
func loadCandidateFiles(t *testing.T, root string, keys []string) []repoSourceFile {
	t.Helper()
	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[k] = true
	}
	var out []repoSourceFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || !consumerExtensions[filepath.Ext(path)] {
			return nil
		}
		// The declaring spec is not a consumer of itself.
		if strings.Contains(path, string(filepath.Separator)+"settings"+string(filepath.Separator)+"spec_") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		f := repoSourceFile{rel: rel, body: string(b), seedOnly: map[string]bool{}}
		for _, line := range strings.Split(f.body, "\n") {
			if seedValueLine.MatchString(line) {
				for k := range wanted {
					if strings.Contains(line, k) {
						f.seedOnly[k] = true
					}
				}
			}
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return out
}

// consumersOf returns the files that READ the key, i.e. mention it on a line
// that is not a settings-table seed tuple.
//
// Matching is exact substring, never a regexp: keys contain dots, and
// `handoff.threshold` would otherwise match `handoff_threshold` — which is how
// the first version of this census reported a truly dead setting as live.
func consumersOf(files []repoSourceFile, key string) []string {
	var hits []string
	for _, f := range files {
		if !strings.Contains(f.body, key) {
			continue
		}
		if f.seedOnly[key] {
			continue
		}
		// The key appears on some line that is not a seed tuple → a real read.
		for _, line := range strings.Split(f.body, "\n") {
			if strings.Contains(line, key) && !seedValueLine.MatchString(line) {
				hits = append(hits, f.rel)
				break
			}
		}
	}
	sort.Strings(hits)
	return hits
}

// knownUnwiredSettings is the allowlist. Each entry must say why the setting is
// still unwired, so a reader can tell "deliberate" from "forgotten".
//
// Self-shrinking: an entry whose key has gained a consumer turns the gate red.
var knownUnwiredSettings = map[string]string{
	// ── lifecycle.*_ttl_days：被迁移 391 播种进设置表，值可存、界面可见，全仓无人读 ──
	// 5 个设置与 5 张表严格一一对应，而 drop_old_state_partitions() 覆盖的
	// 恰好是另外 5 张表，一张都不在这 5 张里。
	"lifecycle.request_wal_ttl_days":                   "P2：对应 request_wal（375MB/975,153 行）；DELETE 还因 request_wal_2026_08 是 columnar 而无法规划",
	"lifecycle.usage_ledger_ttl_days":                  "P2：对应 usage_ledger（1001MB/2,038,536 行）；无任何清理路径",
	"lifecycle.credential_model_call_history_ttl_days": "P2：对应 credential_model_call_history（102MB/284,877 行）；该表是普通表（relkind=r，非分区）",
	"lifecycle.credit_ledger_ttl_days":                 "尚未膨胀（160 kB，0 行），但清理机制同样不存在",
	"lifecycle.tool_usage_stats_ttl_days":              "尚未膨胀（224 kB，0 行），但清理机制同样不存在",

	// ── self_check.*：整个子系统未接线（5 个键成组）──────────────────────
	// D09 对应域。键成组全死通常意味着该子系统的 spec 先落地、接线后延。
	"self_check.enabled":                 "D09：self_check 子系统未接线（该组 5 个键全部无消费方）",
	"self_check.normal_interval_seconds": "D09：同上",
	"self_check.fault_interval_seconds":  "D09：同上",
	"self_check.max_models":              "D09：同上",
	"self_check.max_tokens_per_run":      "D09：同上",

	// ── sessions_v2.*：点号形式全部未接线（下划线形式是活的）───────────────
	// 活的键是 `sessions_v2_compression_read`（下划线），P1-1 的可达性分析用的就是它。
	// 这里 5 个**点号**形式的键从未被读取——声明时用了与活键不同的命名形状。
	"sessions_v2.compression_enabled": "与活键 sessions_v2_compression_read（下划线）命名形状不同，从未接线",
	"sessions_v2.dual_read":           "同上",
	"sessions_v2.l3_read":             "同上",
	"sessions_v2.primary_read":        "同上",
	"sessions_v2.read_timeout_ms":     "同上",

	// ── session.* / log.* / probe.* / 其余 ────────────────────────────────
	"session.cred_history_max":                   "未接线；与 lifecycle.credential_model_call_history_ttl_days 指向同一份数据，但两者都无消费方",
	"session.db_batch_size":                      "未接线",
	"session.db_flush_interval_sec":              "未接线",
	"session.stopped_ttl_minutes":                "未接线",
	"session.ttl_hours":                          "未接线；与 lifecycle 家族同型的 TTL 声明",
	"log.cleanup_enabled":                        "未接线；日志清理实际由 bg/partition_manager.go 的归档 spec 驱动，不由本键控制",
	"log.request_retention_days":                 "未接线",
	"log.trim_days":                              "未接线",
	"probe.partition_cleanup_enabled":            "未接线",
	"probe.promote_batch_size":                   "未接线；promote 批大小实际由迁移里的 p_batch_size 参数决定",
	"disguise.reclaim_idle_seconds":              "未接线",
	"handoff.threshold":                          "未接线；活的是 goal.handoff_signal_threshold_tokens（不同 Key）",
	"lifecycle.promote_interval_hours":           "未接线；promote 周期由 bg 调度器固定",
	"routing_state.capability_substitution_mode": "未接线",
	"security.threat.checks.jailbreak":           "未接线",
	"security.threat.checks.persona_override":    "未接线",
}

// TestData_SettingsSpec_EveryKeyHasAConsumer is the census.
func TestData_SettingsSpec_EveryKeyHasAConsumer(t *testing.T) {
	root := repoRoot(t)
	keys := collectDeclaredKeys(t, root)
	if len(keys) < 200 {
		t.Fatalf("only %d settings keys collected; the corpus shrank or the extractor broke "+
			"(a sweep that quietly finds almost nothing is not evidence of health)", len(keys))
	}

	rawKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		rawKeys = append(rawKeys, k.key)
	}
	files := loadCandidateFiles(t, root, rawKeys)
	if len(files) < 500 {
		t.Fatalf("only %d candidate files scanned; the walker is broken "+
			"(a census over an empty corpus would report every key as unwired)", len(files))
	}

	var unwired, shrunk []string
	var wiredCount, knownDebt int
	for _, sk := range keys {
		consumers := consumersOf(files, sk.key)
		reason, allowlisted := knownUnwiredSettings[sk.key]
		switch {
		case len(consumers) == 0 && allowlisted:
			// Registered debt. Counted separately from "wired" so the summary
			// line cannot imply the setting works.
			knownDebt++
		case len(consumers) == 0:
			unwired = append(unwired, fmt.Sprintf("%s（声明于 %s）", sk.key, sk.declared))
		case allowlisted && reason != "":
			shrunk = append(shrunk, fmt.Sprintf("%s —— 已出现消费方 %v", sk.key, consumers))
		default:
			wiredCount++
		}
	}
	t.Logf("平台设置普查：%d 个 Key = %d 有消费方 + %d 已登记的未接线（已知债）+ %d 未登记",
		len(keys), wiredCount, knownDebt, len(unwired))
	t.Logf("（登记不等于健康：那 %d 条值可存、界面可见、HotReload，却无人读取；"+
		"本门只保证它们不会悄悄增加，以及已接线的会要求收缩登记）", knownDebt)

	for _, u := range unwired {
		t.Errorf("平台设置无任何消费方，且未登记 —— 值可存、界面可见、HotReload，却什么也不做：\n\t%s", u)
	}
	for _, s := range shrunk {
		t.Errorf("登记项已失效（该设置已被接线）——请从 knownUnwiredSettings 删除：\n\t%s", s)
	}
	for key := range knownUnwiredSettings {
		found := false
		for _, sk := range keys {
			if sk.key == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("登记项 %q 在 settings/spec_*.go 里已不存在（设置被删除或改名）——请同步删除登记", key)
		}
	}

	// Accounting invariant, checked LAST and including the shrunk bucket.
	//
	// It used to sit before the per-key errors and omitted `shrunk` from the
	// sum, so a single stale allowlist entry produced a bare
	// "accounting does not add up" with no mention of WHICH key — the gate
	// failed while hiding its own diagnosis. Every key must land in exactly
	// one of the four buckets.
	if wiredCount+knownDebt+len(unwired)+len(shrunk) != len(keys) {
		t.Errorf("账目不平：%d wired + %d known-debt + %d unwired + %d shrunk != %d keys —— "+
			"分类分支有遗漏，请修门的分派逻辑（不要靠删断言来让它变绿）",
			wiredCount, knownDebt, len(unwired), len(shrunk), len(keys))
	}
}
