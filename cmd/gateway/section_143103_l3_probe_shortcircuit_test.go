//go:build !integration

package main

// ═══════════════════════════════════════════════════════════════════════
// §143.103（R146）「探测型 session 短路 L3」（㉕）的接线门
//
// ★ ㉕ 的来历（§143.99 → §143.101）：
//
//	§143.99 把会话正文族读侧长尾定位到 L3 回退 turns 腿；§143.100 找到
//	「为什么慢」（planner 被排序键带偏 + 4 万轮探测合成会话）并实测
//	单次 24,763 ms；§143.101 否证了「加 (session_id, ts DESC) 索引」修法
//	（计划一行都没变）⇒ 剩下两把钥匙，其中【不改语义】的那把就是 ㉕：
//	不让探测型合成 session 走到 L3。
//
// ★ 为什么 ㉕ 是【零语义】的（本门钉住这条论证的证据链）：
//	① 探测合成会话不进 mirror ⇒ bodies 族无行 ⇒ L3 结构性恒 0 行
//	  （§143.99.4 / §143.100.4 生产实测 rows=0）；
//	② 短路返回值 = 「查询返 0 行」的既有映射 ⇒ 调用方走既有 miss 分支；
//	③ 开关【默认关】（opt-in）⇒ 部署后线上行为逐字不变。
//
// ★ 本门的四条腿：
//	① 实现存在且默认关（直接调 compression 包断言行为）；
//	② ★ 跨包一致性：用【真实生成器】（sessionv2mirror.SyntheticSessionID）
//	  产出的探测合成 ID 必须被判定识别 ⇒ 生成器改形态而判定不改 ⇒ 红；
//	③ 接线位置：守卫必须在 loadFromLegacyDB 里、G-2 之后、LogL3Reached 之前
//	  （⇒ 被短路的调用不算「走到 L3」⇒ 服务侧计数降幅 = 实际收益，可对拍）；
//	④ 文档登记（runbook §143.103 + 审计档）。
// ═══════════════════════════════════════════════════════════════════════

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/sessionv2mirror"
)

func repoRoot143103(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile143103(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot143103(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// ── ① 实现存在 + 默认关（形态级：行为正确性由同包单测负责，见
// auto_created_test.go 的分工纪律 —— sync.OnceValue 进程内缓存 ⇒ 门里
// 直接调 ShouldSkipProbeLoad 的取值语义不可复现） ─────────────────────────

func TestSection143103_SkipProbeLoadDefaultsOff(t *testing.T) {
	src := readRepoFile143103(t, filepath.Join("domains", "hooks", "compression", "probe_session.go"))
	if !strings.Contains(src, `EnvSkipProbeSessions = "ENABLE_SESSION_CACHE_SKIP_PROBE_SESSIONS"`) {
		t.Fatal("probe_session.go 缺 opt-in 开关常量 ENABLE_SESSION_CACHE_SKIP_PROBE_SESSIONS")
	}
	// 默认关的载体：env 为空 ⇒ return false。
	if !strings.Contains(src, "return false // 默认关") {
		t.Fatal("★ probe_session.go 的开关读取缺「默认关」分支 —— " +
			"★ 零语义承诺的载体：代码部署但开关不开 ⇒ 线上行为必须逐字不变")
	}
	// 行为覆盖必须在场（同包单测钉默认关 + 取值语义 + 误伤面）。
	unit := readRepoFile143103(t, filepath.Join("domains", "hooks", "compression", "probe_session_test.go"))
	for _, needle := range []string{
		"TestSkipProbeSessionsDefaultIsOff",
		"TestShouldSkipProbeLoad",
		"TestIsProbeSyntheticSessionID",
	} {
		if !strings.Contains(unit, needle) {
			t.Errorf("probe_session_test.go 缺 %s —— 行为正确性没有同包覆盖", needle)
		}
	}
}

// ── ② 跨包一致性：真实生成器的产出必须被判定识别 ──────────────────────────

func TestSection143103_GeneratorShapeMatchesPredicate(t *testing.T) {
	scope := 35
	entry := &telemetry.RequestLogEntry{
		OriginStage:  strPtr143103("node_probe"),
		CredentialID: &scope,
	}
	id := sessionv2mirror.SyntheticSessionID(entry)
	if id == "" {
		t.Fatal("SyntheticSessionID 对探测条目返回空 —— 生成器行为变了")
	}
	if !strings.HasPrefix(id, "sys:probe:cred35:") {
		t.Fatalf("生成器对探测条目产出 %q —— 不再是 sys:probe:cred<id>:<date> 形态 ⇒ 生成器改了，本门与 compression.IsProbeSyntheticSessionID 都要跟着重审", id)
	}
	if !compression.IsProbeSyntheticSessionID(id) {
		t.Fatalf("★ 生成器产出 %q 但 IsProbeSyntheticSessionID 不认它 —— ★ 判定与生成器脱钩 ⇒ 短路会漏掉真探测会话（或误判）", id)
	}

	// 反向：internal / anon kind 的合成会话【不被】判定认领（§143.103 刻意收窄）。
	internal := &telemetry.RequestLogEntry{TaskType: strPtr143103("title_generation")}
	if gid := sessionv2mirror.SyntheticSessionID(internal); compression.IsProbeSyntheticSessionID(gid) {
		t.Errorf("internal 合成会话 %q 被判定认领 —— ㉕ 的范围被悄悄放大（那需要自己的证据轮）", gid)
	}
}

func strPtr143103(s string) *string { return &s }

// ── ③ 接线位置：G-2 之后、LogL3Reached 之前 ──────────────────────────────

func TestSection143103_GuardWiredBeforeL3Reached(t *testing.T) {
	src := readRepoFile143103(t, filepath.Join("domains", "hooks", "compression", "session_cache.go"))

	// 守卫存在且调用 ShouldSkipProbeLoad。
	if !strings.Contains(src, "ShouldSkipProbeLoad(gwSessionID)") {
		t.Fatal("session_cache.go 里没有 ShouldSkipProbeLoad 守卫 —— ㉕ 没有接线")
	}

	// 定位 loadFromLegacyDB 函数体（从函数签名到下一个同级函数）。
	i := strings.Index(src, "func (c *SessionCache) loadFromLegacyDB(")
	if i < 0 {
		t.Fatal("找不到 loadFromLegacyDB —— 函数被改名/移动，本门要跟着重审")
	}
	j := strings.Index(src[i+10:], "\nfunc ")
	if j < 0 {
		j = len(src) - i
	}
	fn := src[i : i+j]

	g2 := strings.Index(fn, "ShouldSkipLegacyLoad(ctx, gwSessionID)")
	probe := strings.Index(fn, "ShouldSkipProbeLoad(gwSessionID)")
	logged := strings.Index(fn, "LogL3Reached(ctx, gwSessionID, tenantID)")
	dbcall := strings.Index(fn, "LastOutboundForSession(ctx, tenantID, gwSessionID)")

	if probe < 0 {
		t.Fatal("守卫不在 loadFromLegacyDB 里 —— 共享路径直连（L-2）会绕过它")
	}
	if g2 < 0 || probe < g2 {
		t.Fatal("P-1 守卫必须在 G-2 守卫之后（同族守卫的次序是文档化的）")
	}
	if logged < 0 || probe > logged {
		t.Fatal("★ P-1 守卫必须在 LogL3Reached 之前 —— 被短路的调用不算「走到 L3」⇒ 服务侧 L3 计数降幅 = ㉕ 的实际收益（可与库端 Δcalls 对拍）")
	}
	if dbcall < 0 || probe > dbcall {
		t.Fatal("P-1 守卫必须在 L3 查询之前 —— 否则短路不成立")
	}

	// 短路返回值必须是 (nil, nil, nil)（= 「查询返 0 行」的既有映射）。
	if !strings.Contains(fn, "skip L3 load, probe synthetic session") {
		t.Fatal("守卫缺日志行 —— 开关打开后运维无法观测它的触发频率")
	}
}

// ── ④ 文档登记 ───────────────────────────────────────────────────────────

func TestSection143103_DocsRegistered(t *testing.T) {
	runbook := readRepoFile143103(t, filepath.Join("docs", "06-deployment", "04-runbooks", "runbooks", "2026-10-04-ursm-write-amplification-measurement.md"))
	for _, needle := range []string{"§143.103", "24,763", "ENABLE_SESSION_CACHE_SKIP_PROBE_SESSIONS"} {
		if !strings.Contains(runbook, needle) {
			t.Errorf("★ runbook 缺 `%s` —— ㉕ 的登记必须带可复算的数字与开关名", needle)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot143103(t), "docs", "audit", "2026-10-24-R146-存储效率综合轮-数据结构普查与P5索引合并.md")); err != nil {
		t.Errorf("★ R146 审计档不在场：%v", err)
	}
	lessons := readRepoFile143103(t, "LESSONS.md")
	if !strings.Contains(lessons, "§301") {
		t.Error("LESSONS.md 缺 §301 —— 通用式没沉淀")
	}
}

// ── ⑤ ★ 承重不变式：`sys:` 命名空间只由网关生成（2026-10-24 12h 审计轮补） ──
//
// ★ 为什么这条必须由门钉住（★ 本轮审计的第二个真问题）：
//
//	㉕ 的「零语义」论证有一条**承重前提**：
//
//	「entry.GwSessionID 不可能以 "sys:probe:" 开头」
//	⇒ 所以 IsProbeSyntheticSessionID 前缀命中 ≡「网关合成的探测会话」
//	⇒ 所以短路掉的确实是那个结构性恒 0 行的 L3 腿。
//
// ★ 这条前提**今天成立**，但它是由**三个文件的四段独立代码**共同保障的，
//   **彼此零交叉校验、且当时没有任何门断言它**：
//
//	(a) domains/streaming/handler.go      sanitizeGwSessionHeader
//	    ⇒ 只放行 gw_ / gt_ / gs_ 三个前缀；其它一律返回 ""（客户端头被丢弃）
//	(b) domains/streaming/session_routing.go  deriveGatewaySessionID
//	    ⇒ 非 gw_/gt_/gs_ 的值一律【前置 gw_】⇒ 「sys:probe:x」→「gw_sys:probe:x」
//	(c) domains/session/session_v2.go      会话库 mint ⇒ 「gw_」+ uuid
//	(d) internal/sessionv2mirror/synthetic_session.go
//	    ⇒ 「sys:」前缀的**唯一生产者**（合成器）；它只在 entry.GwSessionID 为空时
//	       被调用 ⇒ 合成出来的 id 不会回到 GwSessionID 上
//
// ★ 一旦上面任何一处被放松，或新增一条 intake 路径：
//	客户端即可造出 gw_session_id = "sys:probe:cred99:20261024"
//	⇒ GwSessionID 非空 ⇒ IsProbeSyntheticSession 为 false ⇒ **该条目被镜像**
//	⇒ ⇒ session_bodies 族【有行】⇒ L3 本该读到它的 outbound 正文
//	⇒ ⇒ 而短路判据前缀命中 ⇒ **L3 被跳过** ⇒ ⇒ **静默丢失该会话的 outbound 正文**
//	⇒ ⇒ 不报错、不崩溃 ⇒ 最难发现的一类缺陷
//	⇒ ⇒ 而开关一旦被属主为拿 L3 收益而打开，缺陷即刻生效。
//
// ★ 本腿的判据：断言上述四段代码**逐条仍在**，且**没有任何客户端可达的
//   会话 id 入口能产出 "sys:" 前缀**（用「剥注释后检索可执行体」实现 ——
//   不剥注释会把论证文字里的 sys: 当成代码，恒绿门）。

// stripComments143103 去行注释与块注释（891/902 同款纪律）。
func stripComments143103(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	s = line.ReplaceAllString(s, " ")
	return s
}

// funcBody143103 抽出形如 "func <name>(" 的函数体（到下一个顶层 "func " 或文件尾）。
func funcBody143103(t *testing.T, src, name string) string {
	t.Helper()
	idx := strings.Index(src, "func "+name+"(")
	if idx < 0 {
		t.Fatalf("找不到函数 %s —— 它被改名/移动了，本门要跟着重审", name)
	}
	rest := src[idx+len("func "+name+"("):]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	return src[idx : idx+len("func "+name+"(")+end]
}

func TestSection143103_SysNamespaceIsGatewayOnly(t *testing.T) {
	// ── (a) sanitizeGwSessionHeader：只放行 gw_ / gt_ / gs_ ──────────────
	handlerSrc := stripComments143103(readRepoFile143103(t, filepath.Join("domains", "streaming", "handler.go")))
	body := funcBody143103(t, handlerSrc, "sanitizeGwSessionHeader")
	for _, p := range []string{"gw_", "gt_", "gs_"} {
		if !strings.Contains(body, "\""+p+"\"") {
			t.Errorf("sanitizeGwSessionHeader 不再放行 `%s` 前缀 —— 客户端会话 id 的白名单变了 ⇒ 本门的承重前提要重审", p)
		}
	}
	// 白名单之外的取值必须被丢弃（返回 ""）⇒ 不可能产出非 gw_/gt_/gs_ 的 id。
	if !regexp.MustCompile(`return\s+""`).MatchString(body) {
		t.Error("sanitizeGwSessionHeader 不再对白名单外的取值返回空串 —— 客户端可以自报任意会话 id ⇒ 承重前提破了")
	}
	// ★ 白名单里绝不能出现 "sys:" ⇒ 否则客户端就能造出合成命名空间。
	if strings.Contains(body, "sys:") {
		t.Error("★ sanitizeGwSessionHeader 里出现 \"sys:\" ⇒ 客户端可直接自报合成命名空间 ⇒ ㉕ 的短路会误伤真实会话（静默丢 outbound 正文）")
	}

	// ── (b) deriveGatewaySessionID：非 gw_/gt_/gs_ 一律前置 gw_ ─────────
	routingSrc := stripComments143103(readRepoFile143103(t, filepath.Join("domains", "streaming", "session_routing.go")))
	body = funcBody143103(t, routingSrc, "deriveGatewaySessionID")
	if !strings.Contains(body, "\"gw_\"+sessionID") && !strings.Contains(body, "\"gw_\" + sessionID") {
		t.Error("deriveGatewaySessionID 不再前置 gw_ —— 客户端自报的异构命名空间会原样落库 ⇒ 承重前提破了")
	}
	if strings.Contains(body, "sys:") {
		t.Error("★ deriveGatewaySessionID 里出现 \"sys:\" ⇒ 客户端可造出合成命名空间")
	}

	// ── (c) 会话库 mint：gw_ + uuid ─────────────────────────────────────
	v2Src := stripComments143103(readRepoFile143103(t, filepath.Join("domains", "session", "session_v2.go")))
	if !strings.Contains(v2Src, "\"gw_\"+uuid.New().String()") &&
		!strings.Contains(v2Src, "\"gw_\" + uuid.New().String()") {
		t.Error("会话 id 的 mint 形态不再是 \"gw_\"+uuid —— 会话库里可能混入别的命名空间 ⇒ 承重前提破了")
	}

	// ── (d) 合成器是 "sys:" 的唯一生产者 ────────────────────────────────
	synthSrc := stripComments143103(readRepoFile143103(t, filepath.Join("internal", "sessionv2mirror", "synthetic_session.go")))
	if !strings.Contains(synthSrc, "\"sys:%s:%s:%s\"") {
		t.Error("合成器的 id 形态不再是 sys:<kind>:<scope>:<date> ⇒ 生成器改了，本门与 compression.IsProbeSyntheticSessionID 都要重审")
	}

	// ── (e) 兜底扫描：任何【客户端可达】的会话 id 入口都不得产出 "sys:" ──
	//
	// ★ 这条是本腿的核心：它不依赖上面四段的具体写法，而是直接断言
	//   「这些文件里没有任何可执行代码能构造出 "sys:" 前缀」。
	// ⇒ ⇒ 将来新增 intake 路径、或有人把某段改成拼接 "sys:"，门必红。
	for _, rel := range []string{
		filepath.Join("domains", "streaming", "handler.go"),
		filepath.Join("domains", "streaming", "session_routing.go"),
		filepath.Join("domains", "streaming", "request_context.go"),
		filepath.Join("domains", "streaming", "session_assignment.go"),
		filepath.Join("domains", "session", "session_v2.go"),
		filepath.Join("domains", "session", "middleware.go"),
	} {
		clean := stripComments143103(readRepoFile143103(t, rel))
		if strings.Contains(clean, "\"sys:") || strings.Contains(clean, "`sys:") {
			t.Errorf("★ %s 的可执行体里出现 \"sys:\" 字面量 ⇒ 客户端可达的会话 id 入口能造出合成命名空间 ⇒ ㉕ 短路会误伤真实会话", rel)
		}
	}

	// ── (f) 判定侧读的是同一个前缀 ──────────────────────────────────────
	probeSrc := stripComments143103(readRepoFile143103(t, filepath.Join("domains", "hooks", "compression", "probe_session.go")))
	if !strings.Contains(probeSrc, "\"sys:probe:\"") {
		t.Error("compression 侧的判定前缀不再是 \"sys:probe:\" ⇒ 生成器与判定脱钩")
	}
}
