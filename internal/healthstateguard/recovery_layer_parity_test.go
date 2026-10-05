// Package healthstateguard 钉住「admin 恢复入口的状态层覆盖登记表」。
//
// R89-EJ（219 号）为什么要有这个包：
//
// objective 原文要求「**全局的节点状态需要统一，状态更新需要在一个模块中进行，
// 由它来响应多个地方的反馈的请求的正常或失败的结果**」。
// 219 号把这句话落到代码上量了一次，得到的答案是：**进程/Redis 侧确实有一个
// 统一模块（resetInMemoryNodeState），但它是「按需调用的 helper」，不是「必经之路」**
// —— 十个 admin 恢复/重置入口里，**有一个完全没调它**。
//
// ── 本轮坐实的缺口（待裁决 85）──────────────────────────────────────────
//
//	POST /api/admin/diagnostics/routing-blocked/fix  （按供应商批量修复）
//	  admin/diagnostics_routing.go:254 handleRoutingBlockedFix
//
//	它把 DB 侧的闸全清了（credentials / credential_model_bindings /
//	model_probe_state / node_probe_state）+ 失效路由缓存，
//	**但没有调用 resetInMemoryNodeState** —— 于是进程内熔断器、fpslot 的
//	Redis NodeState 冷却、凭据 key 缓存、key rotator 这几层原封不动，
//	而它照样返回 HTTP 200 与 "provider force-recovered"。
//
//	而这个 helper 自己的文档注释（admin/routing.go:1962-1969）写明的正是
//	不调它的后果：
//
//	  "Without this, force_enable / clear_circuit **return HTTP 200 while the
//	   router still filters the node out** until the in-memory cooling windows
//	   expire (breaker cooling / fpslot 300s cooldown / credentialstate Redis
//	   TTL up to 5 min)"
//
//	同族的另外两个恢复入口都做对了：applyForceEnable 清**全部 9 层**（含 URSM v2），
//	handleForceRecoverSingle 在 R21（2026-09-13）审计后补上了进程内三层（:325，
//	注释明写「in-memory circuit cooling 会在『已恢复』之后继续过滤节点 ~5 分钟」是缺陷）。
//	⇒ 批量入口是**唯一没跟上的那一个**，而它恰恰是出事时最该用的那个。
//
// ── 本门是「契约钉桩」，不是「检测活缺陷」──
// 它断言**现状（含缺口）**：批量入口缺的那几层被如实登记为 false 并写明理由，
// 所以现在**是绿的**；谁补上了那几层却不更新登记表，本门会转红，强制同步
// —— 否则下一个接手的人会把「已登记的缺口」当成「已覆盖」。
// （与 216 号对 provenance 接缝、218 号对入站闸的登记是同一种做法。）
package healthstateguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// layer 是一个「状态层」及其在 Go 源码里的可数锚点。
//
// ⚠️ 锚点取的是**调用/赋值符号**，不是列名字面量：
// 列名会出现在只读代码里（例如 admin/routing.go:3425 的
// `if c.CircuitState == "open"` 是管理端列表渲染，不是写入），
// 用列名当锚点会把读者算成写者 —— 这正是 218 号「锚点必须绑定正确对象」那条。
type layer struct {
	// Key 是登记表里的稳定标识。
	Key string
	// Label 是人读的说明。
	Label string
	// Symbols 是「这一层被处理了」的证据符号（任一命中即算处理）。
	// 刻意要求 ≥1 而不是恰好 1：不同入口用不同 helper 达成同一层是合理的。
	Symbols []string
}

// layers 是本轮量出来的全部状态层。
//
// ⚠️ 这张表是 219 号逐个入口读出来的，不是凭印象列的。
var layers = []layer{
	{Key: "dbCredentials", Label: "DB credentials 行（availability/circuit/quota/health 列）", Symbols: []string{"UPDATE credentials", "UPDATE public.credentials"}},
	{Key: "dbBindings", Label: "DB credential_model_bindings（binding 可用性）", Symbols: []string{"UPDATE credential_model_bindings"}},
	{Key: "dbModelProbe", Label: "DB model_probe_state（旧探针系统）", Symbols: []string{"UPDATE model_probe_state"}},
	{Key: "dbNodeProbe", Label: "DB node_probe_state（新探针系统，v_routable 实际读它）", Symbols: []string{"UPDATE node_probe_state"}},
	{Key: "routingCache", Label: "路由候选缓存失效", Symbols: []string{"invalidateRoutingCaches", "InvalidateAllCandidateCache"}},
	{Key: "keyCache", Label: "凭据 API key 进程内缓存失效", Symbols: []string{"InvalidateCredentialKeyCache"}},
	{Key: "keyRotator", Label: "key rotator 状态重置", Symbols: []string{"ResetKeyRotatorForCredential"}},
	{Key: "inProcessNodeState", Label: "进程内熔断器 + fpslot Redis NodeState + credentialstate 缓存（resetInMemoryNodeState 一次清三层）", Symbols: []string{"resetInMemoryNodeState"}},
	{Key: "ursmV2", Label: "URSM v2 Redis 状态", Symbols: []string{"ClearStateForTenant"}},
}

// entry 是一个 admin 恢复/重置入口。
type entry struct {
	// File / Func 定位入口。
	File string
	Func string
	// Route 是注册路径或触发方式（仅供人读，不参与判定）。
	Route string
	// Done 必须对 9 层**逐层表态**（显式 true / false）。
	// 刻意不允许「不表态」：缺席会被下一个接手的人读成「已覆盖」——
	// 这条规则在 219 号当场抓到了本表自己 5 个条目的漏写。
	Done map[string]bool
	// Gaps 写明每一层为什么没处理。**缺一层而不写理由 ⇒ 门红**：
	// 「没做」必须能区分「有意」与「遗漏」。
	Gaps map[string]string
}

// notCredentialRecovery 是那些「名字像恢复、但与凭据健康状态无关」的层的统一理由。
// 刻意写具体：说清它重置的是什么，下一个接手的人才能判断「这条理由还成不成立」。
const notCredentialRecovery = "本入口只重置 credentials 行的状态列，与该层无关"

// ⚠️ 登记表是 219 号在当前 HEAD（21a8994ee）上逐个函数读出来的。
// 每条 Done 的取值由同一套解析逻辑实测生成后逐条人工复核。
// 新增恢复入口必须在此登记，否则 TestEveryRecoveryEntryIsRegistered 转红。
var registry = []entry{
	{
		File: "../../admin/routing.go", Func: "applyForceEnable",
		Route: "emergency-repair 的 force_enable 分支（:5703 执行文件级 const forceEnableCredentialSQL）",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": true, "dbModelProbe": true, "dbNodeProbe": true,
			"routingCache": true, "keyCache": true, "keyRotator": true,
			"inProcessNodeState": true, "ursmV2": true,
		},
		Gaps: map[string]string{},
	},
	{
		File: "../../admin/diagnostics_credential.go", Func: "handleForceRecoverSingle",
		Route: "POST /api/admin/diagnostics/credential/force-recover",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": true, "dbModelProbe": true, "dbNodeProbe": true,
			"routingCache": true, "keyCache": true, "keyRotator": true,
			"inProcessNodeState": true, "ursmV2": false,
		},
		Gaps: map[string]string{
			"ursmV2": "未清 URSM v2 Redis。R21（2026-09-13）只把进程内三层列为缺陷，" +
				"URSM v2 是否应一并清不在该轮结论里 ⇒ 登记为未知，不代拍",
		},
	},
	{
		File: "../../admin/routing.go", Func: "handleEmergencyRepair",
		Route: "POST /api/routing/emergency-repair",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": true, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": true, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": true, "ursmV2": true,
		},
		Gaps: map[string]string{
			"dbModelProbe": "不重置 model_probe_state（旧探针系统）。批量入口重置了它而本入口没有，" +
				"本轮**未核实**旧探针系统是否还有消费面 ⇒ 登记为未知",
			"dbNodeProbe": "不重置 node_probe_state（新探针系统，v_routable 的合取里 nps 那一项未清）" +
				"⇒ 清了 credentials 也可能仍不可路由。**本轮未追到底**，登记为待查",
			"keyCache":   "不清凭据 key 缓存（单凭据与批量恢复入口都清）",
			"keyRotator": "不重置 key rotator（同上）",
		},
	},
	{
		File: "../../admin/provider_offer_force_recover.go", Func: "handleForceRecover",
		Route: "provider-offer 维度强制恢复（:740 调 resetInMemoryNodeState）",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": true, "keyCache": true, "keyRotator": true,
			"inProcessNodeState": true, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbBindings":   "不重置 binding 层（本轮未逐条核对其 cmb 更新是否在别处）",
			"dbModelProbe": "不重置旧探针状态",
			"dbNodeProbe":  "不重置新探针状态（同 handleEmergencyRepair 的疑问）",
			"ursmV2":       "不清 URSM v2 Redis",
		},
	},
	{
		// 待裁决 85 已由 R46 轮（2026-10-05）收口：批量入口接上了与
		// handleForceRecoverSingle 同形的进程内恢复链（步骤 0 先枚举与
		// UPDATE 同 WHERE 的目标凭据，步骤 5 逐凭据清 key 缓存 / key
		// rotator / resetInMemoryNodeState）。URSM v2 维持与单凭据入口
		// 一致的不代拍立场。
		File: "../../admin/diagnostics_routing.go", Func: "handleRoutingBlockedFix",
		Route: "POST /api/admin/diagnostics/routing-blocked/fix",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": true, "dbModelProbe": true, "dbNodeProbe": true,
			"routingCache": true, "keyCache": true, "keyRotator": true,
			"inProcessNodeState": true, "ursmV2": false,
		},
		Gaps: map[string]string{
			"ursmV2": "与 handleForceRecoverSingle 同立场：URSM v2 是否应随批量恢复一并清" +
				"仍无 Owner 裁决，不代拍",
		},
	},
	{
		File: "../../admin/provider_cred_lifecycle.go", Func: "batchRecoverCredentials",
		Route: "批量恢复（凭据生命周期面）",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": true, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": false, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbBindings":   notCredentialRecovery + "（只动 credentials 行）",
			"dbModelProbe": "不重置旧探针状态",
			"dbNodeProbe":  "不重置新探针状态（⚠️ 与 v_routable 的 nps 合取相关，同上待查）",
			"keyCache":     "不清 key 缓存",
			"keyRotator":   "不重置 key rotator",
			"inProcessNodeState": "🔴 与待裁决 85 同族：批量恢复未清进程内熔断器与 fpslot。" +
				"本轮**未核实**该入口是否也对外承诺「已恢复」语义 ⇒ 只登记事实，不并案定性",
			"ursmV2": "不清 URSM v2",
		},
	},
	{
		File: "../../admin/provider_cred_lifecycle.go", Func: "resetCredentialAvailability",
		Route: "单凭据可用性恢复",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": true, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": false, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbBindings":   notCredentialRecovery,
			"dbModelProbe": "不重置旧探针状态",
			"dbNodeProbe":  "不重置新探针状态",
			"keyCache":     "不清 key 缓存",
			"keyRotator":   "不重置 key rotator",
			"inProcessNodeState": "availability_state 与进程内熔断器是两个独立闸门，" +
				"本入口只清前者 ⇒ 清完仍可能不被路由",
			"ursmV2": "不清 URSM v2",
		},
	},
	{
		File: "../../admin/provider_cred_lifecycle.go", Func: "resetCredentialQuota",
		Route: "单凭据配额恢复",
		Done: map[string]bool{
			"dbCredentials": true, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": true, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": false, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbBindings":         notCredentialRecovery,
			"dbModelProbe":       "不重置旧探针状态",
			"dbNodeProbe":        "不重置新探针状态",
			"keyCache":           "不清 key 缓存",
			"keyRotator":         "不重置 key rotator",
			"inProcessNodeState": "配额与进程内熔断器是独立闸门 ⇒ 只清配额不足以让凭据重新被路由",
			"ursmV2":             "不清 URSM v2",
		},
	},
	{
		File: "../../admin/probe_history.go", Func: "handleNodeProbeStateReset",
		Route: "探测历史页的 node_probe_state 重置",
		Done: map[string]bool{
			"dbCredentials": false, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": true,
			"routingCache": true, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": false, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbCredentials": "本入口只重置 node_probe_state 一张表，与 credentials 行的状态列无关",
			"dbBindings":    "不重置 binding 层（v_routable 的合取里 cmb.available 是独立一项）",
			"dbModelProbe":  "不重置 model_probe_state",
			"keyCache":      "不清 key 缓存",
			"keyRotator":    "不重置 key rotator",
			"inProcessNodeState": "不重置进程内熔断器/fpslot；本轮**未核实**该入口是否也对外承诺" +
				"「已恢复」语义 ⇒ 只登记事实",
			"ursmV2": "不清 URSM v2",
		},
	},
	{
		File: "../../admin/routing.go", Func: "resetInMemoryNodeState",
		Route: "（helper 本身，被上面几个入口调用）",
		Done: map[string]bool{
			"dbCredentials": false, "dbBindings": false, "dbModelProbe": false, "dbNodeProbe": false,
			"routingCache": false, "keyCache": false, "keyRotator": false,
			"inProcessNodeState": true, "ursmV2": false,
		},
		Gaps: map[string]string{
			"dbCredentials": "它就是「进程/Redis 层」那个 helper 本身：DB 事务由调用方自己做（职责单一，不是缺陷）",
			"dbBindings":    "同上游：binding 层由调用方处理",
			"dbModelProbe":  "同上游",
			"dbNodeProbe":   "同上游",
			"routingCache":  "不失效路由缓存（由调用方做）",
			"keyCache":      "不失效 key 缓存（由调用方做）",
			"keyRotator":    "不重置 key rotator（由调用方做）",
			"ursmV2":        "不清 URSM v2（调用方自己做，emergency-repair 在 :1851-1857）",
		},
	},
}

// adminDir 是被扫描的目录。
const adminDir = "../../admin"

var (
	funcHeaderRe  = regexp.MustCompile(`^func\s`)
	funcDeclRe    = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z0-9_]+)\s*\(`)
	constDeclRe   = regexp.MustCompile("(?m)^const\\s+([A-Za-z0-9_]+)\\s*=\\s*`")
	recoverNameRe = regexp.MustCompile(`(?i)(recover|repair|reset|restore|enable|blocked)`)
)

// touchesAnyLayer 判断函数体是否命中至少一层符号。
func touchesAnyLayer(body string) bool {
	for _, l := range layers {
		for _, sym := range l.Symbols {
			if strings.Contains(body, sym) {
				return true
			}
		}
	}
	return false
}

// readFile 读一个文件，失败即测试失败（绝不静默跳过 —— 跳过比红更坏）。
func readFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// splitBodies 把一个 Go 文件按顶层 `func ` 切开，返回 函数名 → 函数体（含声明行）。
func splitBodies(src string) map[string]string {
	lines := strings.Split(src, "\n")
	out := map[string]string{}
	cur := ""
	var buf []string
	flush := func() {
		if cur != "" && len(buf) > 0 {
			out[cur] = strings.Join(buf, "\n")
		}
	}
	for _, ln := range lines {
		if funcHeaderRe.MatchString(ln) {
			flush()
			buf = nil
			cur = ""
			if m := funcDeclRe.FindStringSubmatch(ln); m != nil {
				cur = m[1]
			}
		}
		if cur != "" {
			buf = append(buf, ln)
		}
	}
	flush()
	return out
}

// constBodies 抽出一个文件里所有反引号字符串常量的名字与内容。
//
// ⚠️ 为什么要做「常量内联」：admin/routing.go:1595 的 forceEnableCredentialSQL
// 是一段**文件级 const**，执行它的函数 applyForceEnable 的函数体里**根本没有**
// 那几列的字面量（只有 `tx.Exec(ctx, forceEnableCredentialSQL, ...)`）。
// 不内联常量，判据会把**唯一一处「一键清全部健康列」的 SQL** 判成「没写健康列」
// ⇒ 与 218 号「下限必须钉在宽集合」同族。
func constBodies(src string) map[string]string {
	out := map[string]string{}
	for _, m := range constDeclRe.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		rest := src[m[1]:]
		end := strings.Index(rest, "`")
		if end < 0 {
			continue
		}
		out[name] = rest[:end]
	}
	return out
}

// effectiveBody = 函数体 + 它引用到的文件级常量内容。
func effectiveBody(body string, consts map[string]string) string {
	var sb strings.Builder
	sb.WriteString(body)
	for name, val := range consts {
		if strings.Contains(body, name) {
			sb.WriteString("\n")
			sb.WriteString(val)
		}
	}
	return sb.String()
}

// TestEveryRecoveryEntryIsRegistered 是覆盖面下限：
// admin 包里每一个**名字像恢复入口、且确实碰了状态层**的顶层函数，都必须登记。
//
// 发现口径的三版迭代全部记录在案（每一版都被实测推翻）：
//  1. 「同一行同时有健康列字面量与 SET」⇒ **漏掉 handleEmergencyRepair**
//     （Go 的 SQL 多行排版，`SET` 与列名不在同一行）。
//  2. 「函数体同时有 `UPDATE ` 与健康列字面量」⇒ 扫出 32 个，其中 5 个**一层都没碰**。
//  3. 「命中任一层符号」⇒ 扫出 **41 个**，其中 37 个只能盖上橡皮图章
//     ⇒ **登记表一旦掺假就没人信它**，而 41 行里 37 行无信息量正是掺假。
//
// 现行「名字像恢复入口 ∧ 命中至少一层」实测收敛到 **10 个**。
//
// ⚠️ 已知残余盲区：一个**名字不含** recover/repair/reset/restore/enable/blocked
// 的恢复入口能逃掉 ⇒ 登记为已知边界，不假装闭合。
//
// 判据方向是「磁盘 → 登记表」而不是反向，因为反向无法发现
// **新增的恢复入口忘了登记** —— 而那正是最需要被看见的失效方向。
func TestEveryRecoveryEntryIsRegistered(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(adminDir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("扫不到 %s/*.go：%v", adminDir, err)
	}

	registered := map[string]bool{}
	for _, e := range registry {
		k := e.File + "|" + e.Func
		if registered[k] {
			t.Errorf("登记表里 %s 出现两次", k)
		}
		registered[k] = true
	}

	var found, missing []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := readFile(t, f)
		consts := constBodies(src)
		rel, _ := filepath.Rel(filepath.Join(adminDir, ".."), f)
		rel = "../../" + filepath.ToSlash(rel)
		for name, body := range splitBodies(src) {
			if !recoverNameRe.MatchString(name) {
				continue
			}
			eff := effectiveBody(body, consts)
			if !touchesAnyLayer(eff) {
				continue
			}
			key := rel + "|" + name
			found = append(found, key)
			if !registered[key] {
				missing = append(missing, key)
			}
		}
	}
	sort.Strings(found)
	sort.Strings(missing)

	// 下限按逐文件实测取紧：219 号在当前 HEAD 上量到 10 个。
	// 掉一个就红（解析器退化 / 常量内联失效）；多一个则由 missing 分支接手。
	const floor = 10
	if len(found) < floor {
		t.Fatalf("只扫出 %d 个恢复入口，低于下限 %d —— 扫描器退化了，此时「全部已登记」是空断言。扫到的是：%v",
			len(found), floor, found)
	}
	t.Logf("admin 包里「名字像恢复入口且碰了状态层」的函数 %d 个（下限 %d）：%v", len(found), floor, found)

	if len(missing) > 0 {
		t.Errorf("以下 admin 恢复入口改了状态层但没有登记进 healthstateguard.registry：\n  %s\n"+
			"请为它补一条 entry，并逐层声明它清了哪些状态层（没清的必须写理由）——"+
			"「新增恢复入口忘了清进程/Redis 侧」正是这道门要拦的东西",
			strings.Join(missing, "\n  "))
	}
}

// TestRecoveryLayerDeclarationsMatchCode 核对每条登记的层声明与代码一致：
// 声明做了 ⇒ 锚点在函数体（含量内联的常量）里出现 ≥1 次；声明没做 ⇒ 恰好 0 次，
// 且必须在 Gaps 里写明理由。**9 层必须全部表态**，少一层即红
// —— 「没表态」会被下一个接手的人读成「已覆盖」。
func TestRecoveryLayerDeclarationsMatchCode(t *testing.T) {
	cache := map[string]string{}
	load := func(rel string) string {
		if s, ok := cache[rel]; ok {
			return s
		}
		s := readFile(t, rel)
		cache[rel] = s
		return s
	}

	for _, e := range registry {
		name := e.File + "|" + e.Func
		src := load(e.File)
		body, ok := splitBodies(src)[e.Func]
		if !ok {
			t.Errorf("%s：在 %s 里找不到顶层函数 %s —— 登记表过期，或函数被改名/内联了",
				name, e.File, e.Func)
			continue
		}
		eff := effectiveBody(body, constBodies(src))

		for _, l := range layers {
			hits := 0
			for _, sym := range l.Symbols {
				hits += strings.Count(eff, sym)
			}
			declared, stated := e.Done[l.Key]
			if !stated {
				t.Errorf("%s：%q 这一层没有表态 —— 必须写 true 或 false。"+
					"「没表态」会被下一个接手的人读成「已覆盖」", name, l.Key)
				continue
			}
			switch {
			case declared && hits == 0:
				t.Errorf("%s：登记说清了 %q（%s），但函数体里 %v 一次都没出现。"+
					"要么登记表过期了，要么这一层是被别的方式处理的（请改锚点，不要改声明）",
					name, l.Key, l.Label, l.Symbols)
			case !declared && hits > 0:
				t.Errorf("%s：登记说**没有**清 %q（%s），但函数体里 %v 出现了 %d 次 —— "+
					"代码已经变了。若这是有意补上的，请把 Done 改成 true 并删掉 Gaps 里的理由",
					name, l.Key, l.Label, l.Symbols, hits)
			case !declared:
				if strings.TrimSpace(e.Gaps[l.Key]) == "" {
					t.Errorf("%s：%q 未处理却没有在 Gaps 里写理由 —— "+
						"「没做」必须能区分「有意」与「遗漏」，否则下一个人无从判断", name, l.Key)
				}
			}
		}

		// 反向：Done/Gaps 里出现表里没有的层名 ⇒ 拼写漂移，断言会静默失效。
		for k := range e.Done {
			if !knownLayer(k) {
				t.Errorf("%s：Done 里有未知层名 %q（layers 表里没有）—— 这个键的断言不会执行", name, k)
			}
		}
		for k := range e.Gaps {
			if !knownLayer(k) {
				t.Errorf("%s：Gaps 里有未知层名 %q —— 这个理由不会被任何断言检查", name, k)
			}
		}
	}
}

func knownLayer(key string) bool {
	for _, l := range layers {
		if l.Key == key {
			return true
		}
	}
	return false
}
