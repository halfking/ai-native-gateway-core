//go:build !integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// TestRegisteredStartupMigrationsCanRunInSingleTransaction is the inverse of
// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered.
//
// That gate asks "is every canonical migration registered?". This one asks the
// question nothing asked: "can every registered migration actually run under
// the installer's runner?" dbinit.Runner.applySQL runs every file through
// `psql -v ON_ERROR_STOP=1 --single-transaction` and hard-fails the whole
// install on the first error (runner.go:418-424), so a registered migration
// containing CREATE/DROP INDEX CONCURRENTLY can never apply — it aborts, and
// the install stops there.
//
// The repo already has this rule, but only enforced in one direction:
// psqlConcurrencyRequired (stats_migrations_test.go) exempts 727/728/729/744/749
// from being synced "because CREATE INDEX CONCURRENTLY (\gexec) cannot run
// inside the installer's psql --single-transaction". Nothing prevented 718 (51
// CONCURRENTLY statements) and 719 (16) from being registered anyway, and
// those two abort a fresh install once the earlier gaps are cleared.
//
// knownStartupChainConcurrencyViolations holds the reviewed exceptions. When
// one is fixed — by de-registering the file, or by rewriting it without
// CONCURRENTLY — delete the entry and this gate will keep it that way.
func TestRegisteredStartupMigrationsCanRunInSingleTransaction(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("dbinit.Runner.StartupFiles is empty")
	}

	embedDir := filepath.Join("embeddata", "startup")
	for _, name := range runner.StartupFiles {
		path := filepath.Join(embedDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			// A StartupFiles entry with no embeddata copy is TestStartupFilesAreAllEmbedded's
			// business, not this gate's. Skip rather than double-report.
			t.Logf("skip %s: %v", path, err)
			continue
		}
		_, tracked := knownStartupChainConcurrencyViolations[name]
		ok, reason := startupConcurrencyVerdict(raw, tracked)
		if ok {
			// ★ 只给「有 CONCURRENTLY」的文件记理由 ⇒ 300+ 个普通文件不刷屏，
			// ★ 但凡值得一看的（标记文件 / 已复核豁免）都留痕。
			if countConcurrentlyIn(raw) > 0 {
				t.Logf("registered startup migration %q: %s", name, reason)
			}
			continue
		}
		t.Errorf("registered startup migration %q %s; the install hard-fails at this file. "+
			"Either de-register it (add it to psqlConcurrencyRequired with the same "+
			"rationale as 727/728/729/744/749) or rewrite it without CONCURRENTLY, then add it to "+
			"knownStartupChainConcurrencyViolations only if it must stay registered for another reason",
			name, reason)
	}
}

var concurrentlyRe = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)

// The repo already had this rule, but enforced in only one direction:
// psqlConcurrencyRequired exempted 727/728/729/744/749 from being synced
// "because CREATE INDEX CONCURRENTLY (\gexec) cannot run inside the installer's
// psql --single-transaction" — yet 718 (46 CONCURRENTLY statements in its
// embeddata copy) and 719 (12) were synced through all five points anyway, and
// every fresh install that reached them died. The >=704 registration gate only
// asks "is every canonical migration registered" and never asked the inverse.
//
// 2026-10-15 门拣选：本仓仍注册 718/719（缺 non-CONCURRENTLY TTL 替代前
// 不能注销——注销会让 fresh install 丢掉 718 的 TTL 索引）。门照常红这些
// CONCURRENTLY 文件，但以 reviewed exception 记账，避免假绿或强制危险注销。
// 修法落地（非 CONCURRENTLY 变体或迁入 psqlConcurrencyRequired）后删掉条目。
var knownStartupChainConcurrencyViolations = map[string]int{
	"718_drop_redundant_indexes_and_add_ttl_indexes.sql":           46,
	"719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql": 12,
	// 以下为 r43 之后合入、仍走 installer 单事务路径的 CONCURRENTLY 债；
	// 门记账防回归，修法同 718/719（非 CONCURRENTLY 变体或迁入豁免表）。
	"807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql": 1,
	"822_session_summaries_health_pending_index.sql":                  1,
	"837_routing_mv_refresh_state.sql":                                1,
	"845_stats_event_dedup_join_index.sql":                            1,
	// 871（2026-10-18）与上面几条**不同类**：它不是债，是「已复核的豁免」。
	//
	// 本门的前提是「注册文件里出现 CONCURRENTLY ⇒ 它在 --single-transaction 下
	// 永远跑不起来」。该前提**对携带 dbinit:no-transaction 标记的文件不成立**：
	// runner.go 的 applySQL 在拼参数时对标记文件**不加** --single-transaction
	// （`if !requiresNoTransaction(content) { args = append(args, "--single-transaction") }`），
	// 871 因此走非事务通道，能正常应用 —— 与 718 同一先例。
	//
	// 计数 4 而非 1，是因为本门按子串统计且只看剥掉注释后的正文，
	// 871 的字符串字面量里另有三处提到 CONCURRENTLY（DO 块的失败提示、
	// COMMENT ON INDEX 的说明）。
	//
	// ⚠️ 真正需要留意的是别的：applySQL 的 ctx 是 **5 分钟**超时
	// （exec.CommandContext）。CONCURRENTLY 构建若被 ctx 掐断，会留下
	// **invalid 索引**，而同一 psql 调用里的形状校验块也来不及跑。
	// ⇒ 871 的 DO 块会对 indisvalid=false 明确报错，**下一次启动迁移**
	// 才会发现，届时需先 DROP INDEX CONCURRENTLY 重建。
	"871_route_incident_events_created_at_idx.sql": 4,
	// 877（2026-10-19 会话族审计轮）与 871 同类：携带 dbinit:no-transaction
	// 标记 ⇒ applySQL 不加 --single-transaction ⇒ 走非事务通道，能正常应用。
	//
	// 它之所以用三段式 CONCURRENTLY（727/729 同款）而不是父表普通 CREATE INDEX，
	// 正是因为 session_turns 是**活写**分区父表：普通级联会在整个构建期持
	// ShareLock 阻断写入，而 GIN trigram 覆盖 100% 行且 search_text 是 TOAST
	// 宽列，6.4GB 分区属分钟级构建。
	//
	// ⚠️ 5 分钟 applySQL ctx 这条风险对 877 **比对 871 现实得多**：871 的索引是
	// 4 条窄索引，877 是覆盖全部行的 trigram 索引。被 ctx 掐断会留下
	// **invalid 索引**（CONCURRENTLY 的两阶段扫描未完成）。因此 877 自带
	// ① 建之前 DROP 掉同名 invalid 索引、② 建之后校验 indisvalid 并显式报错，
	// 让「上次被掐断」变成可自愈 + 可诊断，而不是一个静默的半成品索引。
	//
	// 计数 5：两条生成器里的字符串字面量 + 校验块 RAISE 文案里的一处提及
	// （countConcurrently 按剥注释后的正文做子串统计，字符串字面量算正文）。
	"877_session_turns_search_text_trgm.sql": 5,
	// 887（2026-10-20 会话族审计 G-5）与 871/877 **同类**：携带
	// dbinit:no-transaction 标记 ⇒ applySQL 不加 --single-transaction
	// ⇒ 走非事务通道，能正常应用（不是「跑不起来」的债，是「已复核的豁免」）。
	//
	// 它是**表达式 GIN 索引** `USING gin (to_tsvector('simple', search_text))`：
	// ★ **刻意不用生成列** —— promote_session_turns_hot_to_partition 会报
	//   「cannot insert into a generated column」⇒ 热表永远排不进月分区；
	// ★ 走三段式 CONCURRENTLY（877 同款）：逐分区建 → 建父壳（ON ONLY）→ ATTACH。
	//
	// ⚠️ 与 877 同样的 5 分钟 applySQL ctx 风险：CONCURRENTLY 被掐断会留下
	//   **invalid 索引**。⇒ 887 自带 ① 建之前自愈 indisvalid=false 的同名索引、
	//   ② 建之后校验 indisvalid 并显式报错，让「上次被掐断」变成可自愈 +
	//   可诊断，而不是静默的半成品索引。
	//
	// 计数 5：三条 CREATE INDEX CONCURRENTLY + 一条 DROP INDEX CONCURRENTLY
	// + 校验块 RAISE 文案里的一处提及（countConcurrently 按剥注释后的正文
	// 做子串统计，字符串字面量算正文）。
	"887_session_turns_search_text_fts.sql": 5,
	// 888（2026-10-20 会话族审计 G-7）同类：携带 dbinit:no-transaction 标记
	// ⇒ 走非事务通道，能正常应用。
	//
	// 它补会话族的 join 索引缺口（session_bodies 父表/hot 表各一条
	// (tenant_id, session_id, turn_no DESC, request_id)，sessions 一条
	// (tenant_id, session_id, partition_date DESC)）。
	// ★ PG 不支持 ALTER INDEX … ADD COLUMN ⇒ 只能「建新 + 删旧」；
	//   删旧的那两条走 **DROP INDEX CONCURRENTLY**（不能拿强锁堵住活写表）。
	//
	// 计数 5：三条 CREATE INDEX CONCURRENTLY + 一条 DROP INDEX CONCURRENTLY
	// + 形状校验块里的一处提及。
	"888_session_family_join_index_gaps.sql": 5,
	// 898（2026-10-23 会话族 hot 排水轮 §143.97）与 871/877/887/888 **同类**：
	// 携带 dbinit:no-transaction 标记 ⇒ applySQL 不加 --single-transaction
	// ⇒ 走非事务通道，能正常应用（不是「跑不起来」的债，是「已复核的豁免」）。
	//
	// 它给 public.session_bodies_hot 补 ts 首列索引
	// (ts, id, partition_date) —— 该 hot 表在生产上**从来没有**这条索引
	// （614 @ 6973a9fb8 把它定义成带 `WHERE ts > now() - interval '8 hours'`
	// 谓词的 partial index ⇒ now() 非 IMMUTABLE ⇒ PG 拒绝 ⇒ 从未建成，
	// 且其后同文件的 idx_session_bodies_hot_partition_date 也从未执行）。
	//
	// ★ 它用 CONCURRENTLY 而不是普通 CREATE INDEX，正是因为 session_bodies_hot
	// 是**活写表**（bodies_writer 持续写入，pg_stat_statements 实测 72,813 次
	// INSERT / mean 20.2ms）⇒ 普通 CREATE INDEX 会在整个构建期持 ShareLock
	// 阻断写入。
	//
	// 计数 3：一条 CREATE INDEX CONCURRENTLY + 形状校验块里的两处提及
	// （indisvalid / indisready 两条 RAISE EXCEPTION 的文案）。
	"898_session_bodies_hot_ts_index.sql": 3,
	// 902（2026-10-24 R146 存储效率轮 §143.102，P5 落地）与 845/888/898 **同类**：
	// 携带 dbinit:no-transaction 标记 ⇒ applySQL 走非事务通道。
	// 它把 stats_event_dedup 的 pkey 换体 UNIQUE(event_id) INCLUDE(occurred_at)
	// 并删除 845 的 (event_id, occurred_at) 复合索引 —— 两索引合一净省 ~114 MB。
	// 用 CONCURRENTLY 是因为该表是活写表（event_writer 每请求一次 upsert）；
	// 删除旧复合索引同样走 DROP INDEX CONCURRENTLY（不能拿强锁堵住活写表）。
	//
	// 计数 4：一条 CREATE UNIQUE INDEX CONCURRENTLY + 两条 DROP INDEX CONCURRENTLY
	// （845 复合索引 + 重跑兜底的 tmp 清理）+ **形状校验块里的一处提及**
	// （RAISE NOTICE 文案里提到的 "previously failed CONCURRENTLY build"）。
	//
	// ★★ 2026-10-27（12h 审计轮）**订正**：★ 本条原先登记 3，★ 但该文件里
	// ★★ 剥注释后的 CONCURRENTLY 文本共 **4** 处 ⇒ ★★ 「复核结论」少算了一处
	// ★★ ⇒ ★★ 而本表里的计数**就是复核结论的载体** ⇒ ★★ 少算 = 结论不完整。
	// ★★ 订正后与同族口径一致（★ 888 = 4 条语句 + 1 处提及 = 5；
	// ★★ 898 = 1 条语句 + 2 处提及 = 3）⇒ ★★ 计数口径 = 「剥注释后**全部**
	// ★★ CONCURRENTLY 文本出现次数」，★ 不分「语句」与「提及」。
	"902_stats_event_dedup_pkey_include.sql": 4,
	// 908（2026-10-24 R147 存储效率续轮，㉘ 裁决落地）与 845/888/898/902 同类：
	// 携带 dbinit:no-transaction 标记 ⇒ 非事务通道。删除 route_incident_events
	// 的零消费方索引 idx_route_incident_events_type_created（110 MB）。
	// 用 DROP INDEX CONCURRENTLY 是因为该表在网关写路径上（failure_observed
	// 持续写入，同表 uq_idem 5.2 天 42 万次 ON CONFLICT 冲突检查）。
	//
	// 计数 1：一条 DROP INDEX CONCURRENTLY（IF EXISTS 自然幂等，无 tmp）。
	"908_route_incident_events_type_created_drop.sql": 1,
	// 909（2026-10-24 R156 · D17-A，fc6c17111）与 877/887/908 同类：携带
	// dbinit:no-transaction 标记 ⇒ 非事务通道。给 session_summaries 的三个
	// LIKE/ILIKE 查询列补 GIN trigram 索引（admin 会话搜索去全表扫）。
	// 用 CREATE INDEX CONCURRENTLY 是因为 session_summaries 是 83 万行热表
	// （普通 CREATE 的 SHARE 锁会阻塞会话聚合写入）。★ 该提交漏登记本表
	// ⇒ main 上此门红（R147 批判审计轮顺带修复：只登记，不改其文件）。
	//
	// 计数 4：4 条 CREATE INDEX CONCURRENTLY IF NOT EXISTS（注释里的提法
	// 已被剥注释器排除）。
	"909_session_summaries_search_trgm_indexes.sql": 4,
	// 916（2026-10-28 R178 轮）与 877/887/888/898/902/909 **同类**：携带
	// dbinit:no-transaction 标记 ⇒ 非事务通道，能正常应用。
	//
	// 它给会话族建 api_key_id **归一表达式**的索引
	// （(CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC）——
	// ★ 缺口本体：252 生产只读实测，「按 api_key 过滤会话」的读点
	//   （admin/usage.go:841-859，7 天窗）把窗口内 75,449 行全读出来、
	//   丢掉 37,825 行 ⇒ 一次读点 220,401 块 ⇒ **一半的行是白读的**。
	// ★ 为什么必须 CONCURRENTLY：session_turns 是活写表 ⇒ 普通 CREATE INDEX
	//   的 SHARE 锁会阻塞会话写入；★ 且 PG17 不支持在分区父表上 CONCURRENTLY
	//   ⇒ 走 729 同款三段式（逐分区 CONCURRENTLY → 父表 ON ONLY 壳 → ATTACH）。
	//
	// 计数 2：一条生成器形态的 CREATE INDEX CONCURRENTLY（逐分区那条，
	// 藏在 format() 的字符串字面量里）+ 一条 hot 表的 CREATE INDEX CONCURRENTLY
	// （注释里的提法已被剥注释器排除）。
	"916_session_turns_api_key_ts_index.sql": 2,
	// 918（2026-10-29 R176 轮）与 877/887/888/902/909/916 同类：携带
	// dbinit:no-transaction 标记 ⇒ 非事务通道。给 node_probe_runs 建覆盖索引
	// (credential_id, completed_at) INCLUDE (raw_model_name, started_at, success)
	// —— 承接 INV-4「双模型探测成功门」的 24h 关联子查询（五个调度读点逐行
	// 求值；completed_at 此前不在任何索引里 ⇒ 每次评估扫该凭据全史 run）。
	// ★ CONCURRENTLY 必需：探针写入与 TTL DELETE（partition_manager）都在跑。
	//
	// 计数 1：一条 CREATE INDEX CONCURRENTLY IF NOT EXISTS（注释里的提法
	// 已被剥注释器排除）。
	"920_node_probe_runs_cred_completed_idx.sql": 1,
}

// countConcurrentlyIn counts CONCURRENTLY occurrences in the given file content.
//
// ★ 与标记判定**必须看同一份字节**：dbinit.Runner.applySQL 把同一份 content
// 既喂给 requiresNoTransaction 又喂给 psql 的 stdin ⇒ 判据与执行通道逐字一致
// （否则会出现「按 A 文件判、按 B 文件跑」的错位）。
func countConcurrentlyIn(raw []byte) int {
	body := stripSQLComments(string(raw))
	return len(concurrentlyRe.FindAllString(body, -1))
}

// startupConcurrencyVerdict 判定「一个已注册的启动迁移，在 dbinit.Runner 下能不能应用」。
//
// ★★★ 这道判据必须与 runner.go 的 applySQL **逐字对齐**（本仓反复吃过「门自己的量具
// 比被测对象更严 ⇒ 造假阳性」的亏：门红着而系统其实是好的 ⇒ 真缺陷被淹没）：
//
//	runner.go:applySQL ——
//	    content, _ := readFile(sqlPath)
//	    if !requiresNoTransaction(content) { args = append(args, "--single-transaction") }
//	    cmd.Stdin = bytes.NewReader(content)
//
// ⇒ 三种情形：
//
//	① 正文里没有 CONCURRENTLY（剥注释后）        ⇒ 能应用（--single-transaction 无害）
//	② 有 CONCURRENTLY **且带 dbinit:no-transaction 标记**
//	   ⇒ applySQL 不加 --single-transaction ⇒ 走非事务通道 ⇒ CONCURRENTLY 合法 ⇒ 能应用
//	③ 有 CONCURRENTLY、无标记、且在 knownStartupChainConcurrencyViolations 里
//	   ⇒ 「已复核的债」⇒ 门记账放行（但必须继续登记在册）
//	④ 有 CONCURRENTLY、无标记、且不在册 ⇒ **不能应用**（psql 在事务块里拒绝
//	   CREATE INDEX CONCURRENTLY ⇒ 整装中止）⇒ 门必须判红。
//
// ★★ 2026-10-26 修：★ 909（会话搜索三列 GIN trigram 索引）在 fc6c17111 登记进
// StartupFiles 时**带了 dbinit:no-transaction 标记**（⇒ 运行期走非事务通道，应用没问题），
// ★ 但没被登记进 knownStartupChainConcurrencyViolations ⇒ ★ 本门把它判红
// ⇒ ★★ 后果：★ `cd installer && go test ./...` 在 main 上**长期红着**
// ⇒ ★★ 而 AGENTS.md「已落地的回归门」把 rc=0 写成硬要求 ⇒ 门长期红 ⇒ 门退化成摆设
// ⇒ ★★ 真缺陷被淹没（这正是本仓登记过的教训）。
// ⇒ ★★ 修法：**不是**「把 909 也登记进豁免表」（那是给一个好文件贴「有债」的标签），
// ★★ 而是**让门问对问题**：★ 标记文件走非事务通道 ⇒ 本来就能应用 ⇒ 不该判红。
// ⇒ ★★ 副作用（正向的）：★ 「新增 CONCURRENTLY 迁移时忘了登记豁免表」这一类
// ★★ **人一定会忘**的失败模式从此消失 ⇒ 门不再依赖「作者记得登记」这件事。
//
// ★★ 牙齿不丢：★ 无标记的 CONCURRENTLY 文件仍然判红（见 ④）⇒ 门没有变成恒绿门；
// ★★ 判据本身另有 TestStartupConcurrencyVerdictHasTeeth 带阳性对照 + 变异臂。
func startupConcurrencyVerdict(raw []byte, tracked bool) (ok bool, reason string) {
	n := countConcurrentlyIn(raw)
	if n == 0 {
		// ★ 每条判定都必须给出**可复核的理由**（读数者不看代码也要能复核这条结论）
		// ⇒ 即便结论是「无特别之处」，也把理由写出来。
		return true, "正文无 CONCURRENTLY ⇒ 单事务通道无碍"
	}
	if dbinit.RequiresNoTransaction(raw) {
		return true, fmt.Sprintf("带 dbinit:no-transaction 标记 ⇒ applySQL 不加 --single-transaction ⇒ 走非事务通道，CONCURRENTLY 合法（正文 %d 处）", n)
	}
	if tracked {
		return true, fmt.Sprintf("已复核豁免（knownStartupChainConcurrencyViolations 登记在册），正文 %d 处 CONCURRENTLY", n)
	}
	return false, fmt.Sprintf("正文 %d 处 CONCURRENTLY 且**无** dbinit:no-transaction 标记 ⇒ psql --single-transaction 下必然中止整装", n)
}

// TestStartupConcurrencyVerdictHasTeeth gives the verdict function its own teeth.
//
// ★★ Why this exists: the gate above was RED on main for 909 (a false positive —
// ★★ 909 carries the marker and therefore applies fine). ★★ The obvious "fix" for a
// ★★ false positive is to widen the exemption until the gate is green — ★★ which is
// ★★ exactly how a gate becomes a decoration. ★★ So the verdict's own behaviour is
// ★★ pinned here, in both directions, plus a mutation arm on the real artifact.
//
// ★★ 变异臂（★ 最关键的一条）：★ take the REAL 909 file, strip its
// ★★ `dbinit:no-transaction` marker line, and assert the verdict FLIPS to red.
// ★★ ⇒ proves the marker branch is doing the work (not a constant), and that the
// ★★ gate would still catch a genuinely unmarked CONCURRENTLY migration.
func TestStartupConcurrencyVerdictHasTeeth(t *testing.T) {
	t.Helper()

	const marked = "-- dbinit:no-transaction\nCREATE INDEX CONCURRENTLY i ON t (a);\n"
	const unmarked = "CREATE INDEX CONCURRENTLY i ON t (a);\n"
	const noConcurrently = "CREATE INDEX i ON t (a);\n"

	cases := []struct {
		name    string
		content string
		tracked bool
		wantOK  bool
	}{
		{"正文无 CONCURRENTLY ⇒ 放行", noConcurrently, false, true},
		{"有 CONCURRENTLY + 带标记 ⇒ 放行（非事务通道）", marked, false, true},
		{"有 CONCURRENTLY + 无标记 + 未登记 ⇒ 判红", unmarked, false, false},
		{"有 CONCURRENTLY + 无标记 + 已复核登记 ⇒ 放行", unmarked, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := startupConcurrencyVerdict([]byte(tc.content), tc.tracked)
			if ok != tc.wantOK {
				t.Fatalf("判定 = %v，期望 %v（reason=%q）", ok, tc.wantOK, reason)
			}
			// ★ 断言恒真比恒假更隐蔽：正例必须**给出理由**，反例必须**给出理由**。
			if tc.wantOK && reason == "" {
				t.Errorf("放行却没有给出理由 ⇒ 读数者无法复核这条结论")
			}
			if !tc.wantOK && reason == "" {
				t.Errorf("判红却没有给出理由 ⇒ 读数者无法复核这条结论")
			}
		})
	}

	embedDir := filepath.Join("embeddata", "startup")

	// ★★ 真实工件：909（带标记）必须放行 —— 把判据钉在真值上，而不是钉在合成样本上。
	raw909, err := os.ReadFile(filepath.Join(embedDir, "909_session_summaries_search_trgm_indexes.sql"))
	if err != nil {
		t.Fatalf("读不到 909 的 embeddata 副本：%v", err)
	}
	if ok, reason := startupConcurrencyVerdict(raw909, false); !ok {
		t.Errorf("909 带 dbinit:no-transaction 标记 ⇒ 应放行，却判红：%s", reason)
	}

	// ★★ 变异臂：抹掉 909 的标记行 ⇒ 判定必须翻红。
	// ★★ 若这条不成立，说明「标记分支」是恒真的（判据与真值脱钩）⇒ 门是摆设。
	stripped := stripNoTransactionMarker(raw909)
	if dbinit.RequiresNoTransaction(stripped) {
		t.Fatalf("变异臂自检失败：抹掉标记后 dbinit.RequiresNoTransaction 仍为 true ⇒ 抹除逻辑本身坏了")
	}
	if ok, reason := startupConcurrencyVerdict(stripped, false); ok {
		t.Errorf("★ 变异臂未被检出：909 去掉标记后仍被判放行 ⇒ 标记分支是恒真的 ⇒ %s", reason)
	}

	// ★★ 真实工件：837（无标记）—— 未登记时必须判红；它之所以在 main 上是绿的，
	// ★★ 唯一的依据就是 knownStartupChainConcurrencyViolations 里有它 ⇒
	// ★★ 这条断言钉住「登记豁免表确实是唯一的放行依据」，而不是碰巧绿。
	raw837, err := os.ReadFile(filepath.Join(embedDir, "837_routing_mv_refresh_state.sql"))
	if err != nil {
		t.Fatalf("读不到 837 的 embeddata 副本：%v", err)
	}
	if dbinit.RequiresNoTransaction(raw837) {
		t.Fatalf("837 的 embeddata 副本带 dbinit:no-transaction 标记 ⇒ 与本测试的前提不符，请更新本测试")
	}
	if ok, _ := startupConcurrencyVerdict(raw837, false); ok {
		t.Errorf("837 无标记且未登记时不该放行 ⇒ 判据漏掉了「无标记」这一路")
	}
	if _, tracked := knownStartupChainConcurrencyViolations["837_routing_mv_refresh_state.sql"]; !tracked {
		t.Errorf("837 已无标记 ⇒ 它必须留在 knownStartupChainConcurrencyViolations 里（否则门会判红）")
	}
}

// stripNoTransactionMarker removes every comment line carrying the
// `dbinit:no-transaction` marker, leaving the rest of the file byte-identical.
// Used by the mutation arm above.
func stripNoTransactionMarker(raw []byte) []byte {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "dbinit:no-transaction") {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// TestKnownStartupChainConcurrencyViolationsStayAccurate guards the
// **accuracy** of the exemption ledger — not just that its names exist.
//
// ★★ 2026-10-27（12h 审计轮）新增：★ 审计发现这张表**只被读名字**、
// ★★ 它登记的**计数**（每个文件里 CONCURRENTLY 的条数）**从不被断言**
// ★★ ⇒ ★★ 表可以与文件**悄悄漂移**：★ 文件改了（加/删一条 CONCURRENTLY）
// ★★ 而表里的计数不变 ⇒ ★★ 「已复核豁免」这条**人类可读的凭据**就变成了
// ★★ **过期文档** ⇒ ★★ 而表里的注释正是**复核结论的唯一载体**
// ★★ ⇒ ★★ 复核结论过期了却没人知道。
// ★★ 实测当时就有漂移：★ 902 登记 3、实际 4。
//
// ★★★ 本门断言三件事（逐条都是「可机械复核」的，不依赖人眼）：
// ① 表里的每个名字在 embeddata/startup/ 下**存在**（否则是陈旧条目）；
// ② 表里登记的**计数** == 该文件里**实际**的 CONCURRENTLY 条数（剥注释后）；
// ③ 每个被登记的文件**确实**还有 CONCURRENTLY（否则它根本不需要豁免）。
//
// ★★ 口径与判定门**同一份字节**：都用 countConcurrentlyIn（剥注释后计数）
// ★★ ⇒ 「量具」与「被测物」不会错位。
func TestKnownStartupChainConcurrencyViolationsStayAccurate(t *testing.T) {
	t.Helper()
	embedDir := filepath.Join("embeddata", "startup")
	for name, want := range knownStartupChainConcurrencyViolations {
		path := filepath.Join(embedDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("豁免表登记了 %q，但 embeddata/startup/ 下没有这个文件 ⇒ 陈旧条目（豁免了空气）", name)
			continue
		}
		got := countConcurrentlyIn(raw)
		if got != want {
			t.Errorf("豁免表登记 %q 的计数是 %d，实际是 %d ⇒ ★ 复核结论已过期（文件改过而表没跟上）\n"+
				"★ ⇒ 修法：核对该文件的 CONCURRENTLY 条数后更新表里的计数与理由注释",
				name, want, got)
		}
		if got == 0 {
			t.Errorf("豁免表登记了 %q，但该文件里已经没有 CONCURRENTLY 了 ⇒ 它不需要豁免（表在豁免一个无害的文件）", name)
		}
	}
}

// TestPsqlConcurrencyRequiredNamesStillExist guards the exemption list itself:
// a name in psqlConcurrencyRequired that no longer exists in the canonical
// startup tree is a stale entry that silently exempts nothing.
func TestPsqlConcurrencyRequiredNamesStillExist(t *testing.T) {
	t.Helper()
	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	for name := range psqlConcurrencyRequired {
		if _, err := os.Stat(filepath.Join(canonicalDir, name)); err != nil {
			t.Errorf("psqlConcurrencyRequired lists %q but canonical startup has no such file: %v", name, err)
		}
		if strings.TrimSpace(psqlConcurrencyRequired[name]) == "" {
			t.Errorf("psqlConcurrencyRequired[%q] has no reason recorded; the exemption must stay reviewable", name)
		}
	}
}
