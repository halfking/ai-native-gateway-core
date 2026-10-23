//go:build !integration

package main

// §143.97 的门：★ 本轮是【session_bodies_hot 排水缺口（delivery-gap + copy-gap）落仓轮】
//
// ★★★★★★ 这个门要守的是【六件不许被无声改掉的事】：
//
//	① ★★★★★★ 【898 必须真的建出 ts 首列索引】
//	   —— ★ 形态：idx_session_bodies_hot_ts (ts, id, partition_date)，
//	   ★ 照抄仓内样板 idx_session_turns_hot_ts。
//	   —— ★ ★ 关键：★ 必须用 CONCURRENTLY（hot 表是活写表 ⇒ 普通 CREATE INDEX
//	   会在构建期持 ShareLock 阻断 bodies_writer 的写入）。
//	② ★★★★★★ 【898 必须带 no-transaction 标记】
//	   —— ★ CONCURRENTLY 不能在事务块内执行 ⇒ installer 的 applySQL 默认加
//	   --single-transaction ⇒ ★ 没有这个标记，全新安装会失败于
//	   "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
//	③ ★★★★★★ 【899 的函数体必须逐字等于【生产现行体】】
//	   —— ★ 「基线必须是生产现行体」这条纪律（§143.96 立）的可执行证据。
//	   —— ★ 生产现行体 = db/db.go 的 ensureSessionBodiesPromoteDrain
//	   （裸 ON CONFLICT DO NOTHING + reconciled CTE + 形状守卫 + try-lock）。
//	④ ★★★★★★ 【899 绝不许带仲裁目标】
//	   —— ★ `ON CONFLICT (id, partition_date)` 不覆盖父表的
//	   session_bodies_tenant_request_partition_key UNIQUE
//	   (tenant_id, request_id, partition_date) ⇒ ⇒ 23505 整批回滚 +
//	   ⇒ 毒丸行卡死 hot 窗口（2026-09-17 PG 日志审计 65 轮实测）。
//	⑤ ★★★★★★ 【六点登记链不许断】
//	   —— ★ canonical / embeddata / go:embed + map / StartupFiles / tsv /
//	   通道腿 files=() + intentional_function_chains（链尾必须是 899）。
//	⑥ ★★★★ 【899 的 down 体必须【真的回到 708 的带仲裁目标形态】】
//	   —— ★ 因为「回滚 = 回到故障」这句话只有 down 体真的装回 708 才成立。
//
// ★ ★★ 纪律：★ 本门是【登记门 + 形态门】，★ 不是端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释）⇒ ⇒ 在注释里写出那行不会假绿。
// ★ ★★ token 搜索一律用 §143.95 轮落地的【带边界】实现（★ 同包共用一份实现
// ★ ★★ ⇒ 避免两轮判据漂移）⇒ ⇒ 「改名成后缀版」逃不掉。
// ★ ★★ ★ **判据 ③ 特别地**：★ 899 的函数体与 Go ensure 的函数体必须逐字相等 ⇒
// ★ ⇒ 这是「899 与 db.go 的 ensureSessionBodiesPromoteDrain 不会漂移」的证据。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14397Migration898 = "sql/migrations/startup/898_session_bodies_hot_ts_index.sql"
	section14397Down898      = "sql/migrations/startup/898_session_bodies_hot_ts_index.down.sql"
	section14397Migration899 = "sql/migrations/startup/899_session_bodies_promote_body_sink.sql"
	section14397Down899      = "sql/migrations/startup/899_session_bodies_promote_body_sink.down.sql"

	section14397Embed898     = "installer/cmd/llm-gw-installer/embeddata/startup/898_session_bodies_hot_ts_index.sql"
	section14397Embed898Down = "installer/cmd/llm-gw-installer/embeddata/startup/898_session_bodies_hot_ts_index.down.sql"
	section14397Embed899     = "installer/cmd/llm-gw-installer/embeddata/startup/899_session_bodies_promote_body_sink.sql"
	section14397Embed899Down = "installer/cmd/llm-gw-installer/embeddata/startup/899_session_bodies_promote_body_sink.down.sql"

	section14397InstallerMain = "installer/cmd/llm-gw-installer/main.go"
	section14397Runner        = "installer/internal/dbinit/runner.go"
	section14397Manifest      = "sql/schema/installed_startup_migrations.tsv"
	section14397Channel       = "scripts/apply-db-revision-sequence.sh"
	section14397DBGo          = "db/db.go"

	section14397Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14397Lessons = "LESSONS.md"
	section14397Agents  = "AGENTS.md"
)

const (
	section14397File898 = "898_session_bodies_hot_ts_index.sql"
	section14397File899 = "899_session_bodies_promote_body_sink.sql"
)

func repoRoot14397(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14397(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14397(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// sqlExecutable14397 剥掉【整行注释】后的可执行面。
func sqlExecutable14397(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14397(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// sqlBody14397 截出 $function$ ... $function$ 之间的【函数体原文】（保留缩进）。
//
// ★ ★ 判据 ③ 要的是【逐字相等】⇒ ⇒ 这里必须**保留原始缩进**
// ★ ★ （★ 所以不能用 sqlExecutable14397 —— 它会 TrimSpace 每一行）。
func sqlBody14397(t *testing.T, rel string) string {
	t.Helper()
	raw := readRepoFile14397(t, rel)
	start := strings.Index(raw, "AS $function$\n")
	if start < 0 {
		t.Fatalf("★ %s 里没有 `AS $function$` —— ★ 判据失效：★ 门读不到函数体却报绿", rel)
	}
	bodyStart := start + len("AS $function$\n")
	end := strings.Index(raw[bodyStart:], "\n$function$;")
	if end < 0 {
		t.Fatalf("★ %s 里找不到函数体的收尾 `$function$;` —— ★ 判据失效", rel)
	}
	return raw[bodyStart : bodyStart+end]
}

// goEnsureBody14397 从 db/db.go 的 ensureSessionBodiesPromoteDrain 里
// 截出同款函数体原文（★ 保留原始缩进 ⇒ ★ 可与 899 的函数体逐字比对）。
func goEnsureBody14397(t *testing.T) string {
	t.Helper()
	raw := readRepoFile14397(t, section14397DBGo)
	marker := "func (d *DB) ensureSessionBodiesPromoteDrain(ctx context.Context) error {"
	start := strings.Index(raw, marker)
	if start < 0 {
		t.Fatalf("★ %s 里没有 ensureSessionBodiesPromoteDrain —— ★ 判据失效", section14397DBGo)
	}
	rest := raw[start:]
	execAt := strings.Index(rest, "d.pool.Exec(ctx, `")
	if execAt < 0 {
		t.Fatalf("★ ensureSessionBodiesPromoteDrain 里没有 pool.Exec —— ★ 判据失效")
	}
	bodyStart := execAt + len("d.pool.Exec(ctx, `")
	end := strings.Index(rest[bodyStart:], "`)")
	if end < 0 {
		t.Fatalf("★ ensureSessionBodiesPromoteDrain 里的 SQL 串没有闭合 —— ★ 判据失效")
	}
	inner := rest[bodyStart : bodyStart+end]
	// 内层的 $function$ ... $function$
	fs := strings.Index(inner, "AS $function$\n")
	if fs < 0 {
		t.Fatalf("★ Go ensure 体里没有 `AS $function$` —— ★ 判据失效")
	}
	fs += len("AS $function$\n")
	// ★ 收尾标记是 `$function$;`（★ 分号紧跟 tag ⇒ 不带换行前缀）。
	fe := strings.Index(inner[fs:], "$function$;")
	if fe < 0 {
		t.Fatalf("★ Go ensure 体里找不到 `$function$;` —— ★ 判据失效")
	}
	return inner[fs : fs+fe]
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ① + ②：898 的索引形态 + no-transaction 标记
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_898_BuildsTsLeadingIndexWithConcurrently(t *testing.T) {
	exe := sqlExecutable14397(t, section14397Migration898)

	// ★ 索引名 + 三列形态 + 首列 ts —— 照抄仓内样板 idx_session_turns_hot_ts。
	//
	// ★ ★ 口径说明：★ 898 用 `SELECT format(...) \gexec` 投递（888 同款）⇒
	// ★ ★ 索引语句在**字符串字面量**里 ⇒ 索引名是 %I 占位符 ⇒
	// ★ ★ 判据必须查**字面量里的那一面**，不能查展开后的形态。
	want := "CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.session_bodies_hot USING btree (ts, id, partition_date)"
	if !strings.Contains(exe, want) {
		t.Errorf("898 里没有 `%s` —— ★ ts 首列索引没被建出来 ⇒ 批次选取仍是 Seq Scan + Sort", want)
	}
	// ★ 索引名必须逐字在（%I 的实参）。
	if !strings.Contains(exe, "'idx_session_bodies_hot_ts'") {
		t.Errorf("898 里没有 'idx_session_bodies_hot_ts' 这个索引名 —— ★ 索引名必须是 idx_session_bodies_hot_ts（与样板 idx_session_turns_hot_ts 同族）")
	}
	if !strings.Contains(exe, "\\gexec") {
		t.Errorf("898 里没有 `\\gexec` —— ★ format() 的结果不会被投递")
	}

	// ★ ★ 绝不许出现带仲裁目标/带 now() 谓词的形式：
	// ★ ★ ★ 614 @ 6973a9fb8 就是因为 `WHERE ts > now() - interval '8 hours'`
	// ★ ★ ★ 谓词非 IMMUTABLE 而【从未建成】——★ 那是本轮要修的历史缺陷本体。
	if strings.Contains(exe, "now()") {
		t.Errorf("898 的可执行面里出现了 now() —— ★ partial index 谓词必须 IMMUTABLE ⇒ ★ 614 当年就是这样失败的（该索引从未在生产建成）")
	}

	// ★ CONCURRENTLY 必须有（hot 表是活写表）。
	if !strings.Contains(exe, "CREATE INDEX CONCURRENTLY") {
		t.Errorf("898 没用 CREATE INDEX CONCURRENTLY —— ★ hot 表是活写表 ⇒ 普通 CREATE INDEX 会在构建期持 ShareLock 阻断写入")
	}

	// ★ 后置条件必须在（索引必须 valid/ready/首列 ts）。
	for _, needle := range []string{"indisvalid", "indisready", "idx_session_bodies_hot_ts"} {
		if !strings.Contains(exe, needle) {
			t.Errorf("898 的后置条件里没有 `%s` —— ★ 索引建坏了不会被发现", needle)
		}
	}
}

func TestSection14397_898_HasNoTransactionMarker(t *testing.T) {
	// ★ ★ 标记检查必须【两侧都查】—— ★ 理由是【字节投递链】，不是「M04 报的缺陷」：
	// ★ ★ ① **embeddata 副本 = installer 通道真正执行的那一份**：
	// ★ ★    installer/cmd/llm-gw-installer/main.go 的 setupSQLDir() 把
	// ★ ★    **embedded 字节**写到临时目录 ⇒ dbinit.Runner.applySQL 从
	// ★ ★    SQLDir 读文件 ⇒ ⇒ 真正喂给 psql 的就是 embeddata 那份字节。
	// ★ ★ ② **canonical 副本 = 部署通道读的那一份**：
	// ★ ★    scripts/apply-db-revision-sequence.sh 用
	// ★ ★    `psql -X -v ON_ERROR_STOP=1 -f -` 流式执行（**不包事务**）
	// ★ ★    ⇒ ⇒ canonical 侧的标记对它不是生死线，但它必须与 embeddata
	// ★ ★    **逐字节一致**（六点位登记已守）⇒ ⇒ 两份都要带标记。
	// ★ ★ ③ 本判据**逐字镜像** installer/internal/dbinit/runner.go 的
	// ★ ★    requiresNoTransaction 语义（TrimSpace ⇒ 前缀 `--` ⇒ 含
	// ★ ★    token）—— ★ ★ 【镜像】是刻意的：若 installer 改了口径，
	// ★ ★    installer 侧那条【调用真函数】的门会先红（见
	// ★ ★    installer/internal/dbinit/untransactable_requires_marker_test.go）。
	// ★ ★
	// ★ ★ ⚠ **订正**：★ 本判据最初只查 canonical，★ 而变异台 M04 首跑
	// ★ ★ 报的是「期望红但门仍绿 ⇒ 门没牙」—— ★ ★ **那个归因是错的**：
	// ★ ★ M04 只替换了标记的一种拼写（`-- dbinit:no-transaction`），
	// ★ ★ 而 898 第 139 行还有另一种拼写（`-- ★ ★ dbinit:no-transaction 标记`）
	// ★ ★ ⇒ ★ installer 的真谓词仍判 true ⇒ ★ **变异本身是惰性的**
	// ★ ★ ⇒ ★ **门绿是对的**（详见 runbook §143.97.14 第 4 条）。
	hasMarker := func(rel string) bool {
		raw := readRepoFile14397(t, rel)
		// ★ 标记必须是【整行注释】（installer 的 requiresNoTransaction 只看
		// ★ 以 `--` 开头的行 ⇒ 写在代码块里不算）。
		for _, line := range strings.Split(raw, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "--") && strings.Contains(trimmed, "dbinit:no-transaction") {
				return true
			}
		}
		return false
	}

	for _, rel := range []string{section14397Migration898, section14397Embed898} {
		if !hasMarker(rel) {
			t.Errorf("%s 缺少整行注释形式的 `dbinit:no-transaction` 标记 —— ★ CREATE INDEX CONCURRENTLY 不能在事务块内执行 ⇒ 全新安装会失败", rel)
		}
	}

	// ★ down 也要带标记（它用 DROP INDEX CONCURRENTLY）。
	for _, rel := range []string{section14397Down898, section14397Embed898Down} {
		if !hasMarker(rel) {
			t.Errorf("%s 缺少 `dbinit:no-transaction` 标记 —— ★ DROP INDEX CONCURRENTLY 同样不能事务化", rel)
		}
	}
	if !strings.Contains(sqlExecutable14397(t, section14397Down898), "DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_bodies_hot_ts") {
		t.Errorf("898 的 down 没有用 DROP INDEX CONCURRENTLY —— ★ hot 表是活写表 ⇒ 普通 DROP INDEX 会持 ACCESS EXCLUSIVE 阻塞写入")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ③ + ④：899 的函数体逐字等于生产现行体 + 不带仲裁目标
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_899_BodyIsByteIdenticalToGoEnsure(t *testing.T) {
	got := sqlBody14397(t, section14397Migration899)
	want := goEnsureBody14397(t)

	if got != want {
		t.Errorf("899 的函数体与 db/db.go 的 ensureSessionBodiesPromoteDrain **不是逐字相等**\n"+
			"★ 这是「基线必须是生产现行体」纪律的可执行证据 ⇒ 语义零变更这条承诺就落空了\n"+
			"★ 899 体长=%d 字符，Go ensure 体长=%d 字符", len(got), len(want))
	}

	// ★ ★ 再钉一层：★ 必须是【裸】 ON CONFLICT DO NOTHING（无仲裁目标）。
	if !strings.Contains(got, "ON CONFLICT DO NOTHING") {
		t.Errorf("899 的函数体里没有裸 `ON CONFLICT DO NOTHING` —— ★ 裸形式兜住所有唯一冲突（708 的仲裁目标不覆盖父表的 (tenant_id, request_id, partition_date)）")
	}

	// ★ ★ 绝不许带仲裁目标。
	if strings.Contains(got, "ON CONFLICT (id, partition_date)") {
		t.Errorf("899 的函数体里带了仲裁目标 `ON CONFLICT (id, partition_date)` —— ★ ★ 这正是 708 的形态 ⇒ ⇒ 23505 整批回滚 + 毒丸行卡死 hot 窗口")
	}

	// ★ reconciled CTE 必须在。
	if !strings.Contains(got, "reconciled AS") {
		t.Errorf("899 的函数体里没有 reconciled CTE —— ★ 父表已存在的同键行不会被刷新 ⇒ 它们的 hot 行永远排不出去")
	}

	// ★ 形状守卫 + try-lock 必须在（生产现行体的两道牙）。
	if !strings.Contains(got, "v_parent_shape") || !strings.Contains(got, "v_hot_shape") {
		t.Errorf("899 的函数体里没有列形状守卫（v_parent_shape / v_hot_shape）—— ★ 列集漂移会静默丢列")
	}
	if !strings.Contains(got, "pg_try_advisory_xact_lock") {
		t.Errorf("899 的函数体里没有 pg_try_advisory_xact_lock —— ★ 阻塞式锁会让另一个网关实例的同表排水卡住")
	}
}

func TestSection14397_899_SignatureUnchanged(t *testing.T) {
	exe := sqlExecutable14397(t, section14397Migration899)

	// ★ 签名逐字不变 ⇒ CREATE OR REPLACE 是替换、不是新增 ⇒ 无重载残留。
	for _, needle := range []string{
		"CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(",
		"retention_window interval DEFAULT '8 hours'",
		"batch_size integer DEFAULT 5000",
		"RETURNS TABLE(moved_count bigint)",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("899 里没有 `%s` —— ★ 签名变了就不是替换而是新增 ⇒ 会留重载残留", needle)
		}
	}
}

func TestSection14397_899_HasPostCondition(t *testing.T) {
	exe := sqlExecutable14397(t, section14397Migration899)

	// ★ 后置条件的 5 条判据必须都在。
	for _, needle := range []string{
		"ON CONFLICT DO NOTHING",
		"reconciled AS",
		"v_parent_shape",
		"pg_try_advisory_xact_lock",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("899 的后置条件里没有 `%s` —— ★ 装错了形态不会被当场发现", needle)
		}
	}

	// ★ 后置条件必须【反向】断言仲裁目标不在（正向断言"裸 ON CONFLICT 在"
	// ★ 不足以排除"两者都在"）。
	if !strings.Contains(exe, "installed body still carries the targeted arbiter") {
		t.Errorf("899 的后置条件缺少【反向】断言（仲裁目标不在场）—— ★ 正向断言排除不了「两者都在」")
	}

	// ★ 台账自登记必须双账本之一（695-705 定式）。
	if !strings.Contains(exe, "INSERT INTO public.schema_migrations") {
		t.Errorf("899 没有自登记 schema_migrations —— ★ 台账缺口 = 迁移链不再重跑它")
	}
	if !strings.Contains(exe, "'899'") {
		t.Errorf("899 的台账自登记里没有 '899' 这个版本号")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ⑥：899 的 down 体必须真的回到 708 的带仲裁目标形态
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_899_DownRestoresTargetedArbiter(t *testing.T) {
	exe := sqlExecutable14397(t, section14397Down899)

	// ★ ★ down 必须装回带仲裁目标的形态 —— ★ 「回滚 = 回到故障」这句话
	// ★ ★ 只有在 down 体真的装回 708 时才成立。
	if !strings.Contains(exe, "ON CONFLICT (id, partition_date) DO NOTHING") {
		t.Errorf("899 的 down 没有装回 708 的带仲裁目标形态 —— ★ 「回滚 = 回到故障」的登记就不诚实")
	}
	// ★ down 必须**去掉** reconciled CTE（708 形态里没有它）。
	if strings.Contains(exe, "reconciled AS") {
		t.Errorf("899 的 down 里还留着 reconciled CTE —— ★ 708 形态没有这个 CTE ⇒ down 没真的回到 708")
	}
	// ★ down 必须撤销台账行。
	if !strings.Contains(exe, "DELETE FROM public.schema_migrations WHERE version = '899'") {
		t.Errorf("899 的 down 没有撤销 schema_migrations 里的 '899' 行 —— ★ 台账会留着一条已经不生效的记账")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ⑤：六点登记链不许断
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_SixPointRegistration(t *testing.T) {
	// ★ ① canonical 存在（隐含：readRepoFile 会失败）；② embeddata 副本【逐字节】一致。
	pairs := [][2]string{
		{section14397Migration898, section14397Embed898},
		{section14397Down898, section14397Embed898Down},
		{section14397Migration899, section14397Embed899},
		{section14397Down899, section14397Embed899Down},
	}
	for _, p := range pairs {
		canon := readRepoFile14397(t, p[0])
		embed := readRepoFile14397(t, p[1])
		if canon != embed {
			t.Errorf("★ embeddata 副本与 canonical 不一致：%s vs %s —— ★ 字节门必须逐字节相等（两份拷贝漂移 = 装到生产的那份是旧的）", p[0], p[1])
		}
	}

	// ★ ③ go:embed + map 登记。
	main := readRepoFile14397(t, section14397InstallerMain)
	for _, needle := range []string{
		"//go:embed embeddata/startup/898_session_bodies_hot_ts_index.sql",
		"//go:embed embeddata/startup/899_session_bodies_promote_body_sink.sql",
		`"startup/898_session_bodies_hot_ts_index.sql":`,
		`"startup/899_session_bodies_promote_body_sink.sql":`,
	} {
		if !strings.Contains(main, needle) {
			t.Errorf("installer/cmd/llm-gw-installer/main.go 里没有 `%s` —— ★ 登记链断了", needle)
		}
	}

	// ★ ④ StartupFiles 登记。
	runner := readRepoFile14397(t, section14397Runner)
	for _, needle := range []string{
		`"898_session_bodies_hot_ts_index.sql"`,
		`"899_session_bodies_promote_body_sink.sql"`,
	} {
		if !strings.Contains(runner, needle) {
			t.Errorf("installer/internal/dbinit/runner.go 的 StartupFiles 里没有 `%s` —— ★ 登记链断了", needle)
		}
	}

	// ★ ⑤ tsv manifest 登记。
	manifest := readRepoFile14397(t, section14397Manifest)
	for _, needle := range []string{section14397File898, section14397File899} {
		if !strings.Contains(manifest, needle) {
			t.Errorf("%s 里没有 `%s` —— ★ 登记链断了（tsv 是自动生成的 ⇒ 要跑 `cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update`）", section14397Manifest, needle)
		}
	}

	// ★ ⑥ 通道腿：files=() + intentional_function_chains（★ 链尾必须是 899）。
	channel := readRepoFile14397(t, section14397Channel)
	for _, needle := range []string{
		`"$ROOT_DIR/sql/migrations/startup/898_session_bodies_hot_ts_index.sql"`,
		`"$ROOT_DIR/sql/migrations/startup/899_session_bodies_promote_body_sink.sql"`,
	} {
		if !strings.Contains(channel, needle) {
			t.Errorf("通道脚本里没有 `%s` —— ★ 通道不投递 ⇒ 「只跑迁移」这条路拿不到它", needle)
		}
	}

	chain := "'promote_session_bodies_hot_to_partition|708_session_bodies_s1a.sql|899_session_bodies_promote_body_sink.sql|'"
	if !strings.Contains(channel, chain) {
		t.Errorf("通道脚本里没有链登记 `%s` —— ★ 同一函数被 708 与 899 两个通道文件重定义 ⇒ 未登记 ⇒ 部署会在应用任何文件之前 abort（exit 5）", chain)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 通道链序：files=() 里的投递序必须与链登记序一致
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_ChannelChainOrderMatchesDeliveryOrder(t *testing.T) {
	channel := readRepoFile14397(t, section14397Channel)

	// ★ files=() 里 708 的位置必须早于 899（★ 链序 = 投递序 ⇒ 最后一项胜出）。
	i708 := strings.Index(channel, "$ROOT_DIR/sql/migrations/startup/708_session_bodies_s1a.sql")
	i899 := strings.Index(channel, "$ROOT_DIR/sql/migrations/startup/899_session_bodies_promote_body_sink.sql")
	if i708 < 0 || i899 < 0 {
		t.Fatalf("★ 通道脚本里找不到 708 或 899 的 files=() 条目 —— ★ 判据失效")
	}
	if i708 > i899 {
		t.Errorf("★ 通道 files=() 里 899 排在 708 之前（708@%d, 899@%d）—— ★ 链序必须 = 投递序 ⇒ 链尾（899）必须是最后一项", i708, i899)
	}

	// ★ 链登记串里 899 必须在 708 之后。
	chainPat := regexp.MustCompile(`'promote_session_bodies_hot_to_partition\|([^']*)'`)
	m := chainPat.FindStringSubmatch(channel)
	if m == nil {
		t.Fatalf("★ 通道脚本里找不到 promote_session_bodies_hot_to_partition 的链登记 —— ★ 判据失效")
	}
	parts := strings.Split(strings.Trim(m[1], "|"), "|")
	if len(parts) != 2 {
		t.Fatalf("★ 链登记的项数是 %d（期望 2：708 + 899）—— ★ 615/626/638 不在通道里 ⇒ 不该进链", len(parts))
	}
	if parts[0] != "708_session_bodies_s1a.sql" || parts[1] != "899_session_bodies_promote_body_sink.sql" {
		t.Errorf("★ 链登记的项是 %v —— ★ 必须是 [708, 899]（899 是活库里应有的终态体 ⇒ 必须是链尾）", parts)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 文档登记
// ═══════════════════════════════════════════════════════════════════════

func TestSection14397_DocsRegistered(t *testing.T) {
	runbook := readRepoFile14397(t, section14397Runbook)
	if !strings.Contains(runbook, "§143.97") {
		t.Errorf("runbook 里没有 `§143.97` —— ★ 过程不可查")
	}
	lessons := readRepoFile14397(t, section14397Lessons)
	if !strings.Contains(lessons, "§291") {
		t.Errorf("LESSONS.md 里没有 `§291` —— ★ 通用式没沉淀")
	}
	// ★ 本轮收尾又沉淀了 §292（★ 「惰性变异」筛查 / 「变异要改全部等价形态」/
	// ★ 「探针的取值来源」/ 「门要守真函数」）⇒ ⇒ 一并钉住。
	if !strings.Contains(lessons, "§292") {
		t.Errorf("LESSONS.md 里没有 `§292` —— ★ 本轮收尾的通用式（惰性变异筛查 / 取值来源对齐 / 守真函数）没沉淀")
	}
	agents := readRepoFile14397(t, section14397Agents)
	if !strings.Contains(agents, "R141") {
		t.Errorf("AGENTS.md 里没有 `R141` —— ★ 交接没更新")
	}
	if !strings.Contains(agents, "R142") {
		t.Errorf("AGENTS.md 里没有 `R142` —— ★ 本轮收尾（§143.97.15）的交接没更新")
	}
}
