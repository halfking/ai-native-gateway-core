package startup

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 817 契约：把 816 给 client_ip 投影装的**字符类**守卫换成**语义**守卫
// pg_input_is_valid(v,'inet')。
//
// 为什么要有 817（审计 §9.64）：816 的守卫是
// `t.client_ip ~ '^[0-9a-fA-F:.]+$'`。它只挡得住**非字符集**的垃圾，而下面这批
// **全是合法字符集**，它们通过正则，然后死在 `::inet` 上：
//
//	192.168.1 / deadbeef / 1.2.3.4.5.6 / ::: / ... / 999.1.1.1
//
// 真库实测（PG 17.10）往 session_turns 插一行 client_ip='192.168.1'，再读
// public.request_logs_with_current_month：
//
//	ERROR:  invalid input syntax for type inet: "192.168.1"
//
// 而这**正是 816 声称要防的那场事故**。816 的注释写「守卫把畸形值落 NULL 而不是
// 报错」——那句话对字符类不合法的值成立，对字符类合法但语义非法的值**不成立**，
// 而后者才是真正的攻击面（X-Real-IP 头由客户端自由填写）。
//
// 我在 §9.61 报过一张守卫行为表（garbage / 多跳链 / 空串 全落 NULL），**那张表
// 只测了字符类不合法的值，于是给了「守卫已覆盖」一个过宽的结论**。本文件就是
// 对那次收窄的补偿：判据改成**语义**判据，且必须带反向证据。
//
// 本文件守四件**没人会主动想起**的事：
//  1. 守卫必须是 pg_input_is_valid，**且字符类守卫必须消失**——两者并存等于没修；
//  2. view 链守卫（680 事故形态）不能在重写 816 时被丢掉；
//  3. down 必须真的退回 816 的字符类形态，且必须**带着「这是已知弱形态」的警告**；
//  4. 敌意值行为门必须有**反向证据**——同一批输入在 816 形态下必须报错，否则
//     这道门可能是空的（一个恒过的门比没有门更坏）。
const migration817 = "817_request_logs_view_client_ip_semantic_guard.sql"

// 817HostileClientIPs 分两类，**两类都必须进测试**：
//
//	· 语义非法但字符类合法 —— 816 守卫漏掉的正是这一类（本次修复的靶心）
//	· 语义合法 —— 防止「守卫退化成一律落 NULL」这种「修好了但变全盲」的形状
//	  （§9.18 同一结构：把有源投影换成 NULL 补位，指标全绿而数据全瞎）
var (
	hostileInvalidClientIPs = []string{
		"192.168.1",            // 三段，不是四段
		"deadbeef",             // 8 个 hex 字符，但不是点分四段
		"1.2.3.4.5.6",          // 六段
		":::",                  // IPv6 分隔符但无任何地址
		"...",                  // 纯点
		"999.1.1.1",            // 段值越界
		"2a06:98c0:3600::103x", // 尾部多一个字符
	}
	hostileValidClientIPs = []string{
		"1.2.3.4",
		"0.0.0.0",
		"2a06:98c0:3600::103",
		"::1",
	}
)

func readMigration817(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(b)
}

// TestMigration817UsesSemanticClientIPGuard 静态门：守卫形态必须钉在语义上。
func TestMigration817UsesSemanticClientIPGuard(t *testing.T) {
	src := readMigration817(t, migration817)
	proj := between(t, src, "proj := $proj$", "$proj$;")

	// ① 语义守卫必须在，且必须包住那次转换。
	if !strings.Contains(proj, "pg_input_is_valid(t.client_ip, 'inet')") {
		t.Errorf("817 的 client_ip 投影没有用 pg_input_is_valid(v,'inet')。\n"+
			"816 的字符类守卫 `^[0-9a-fA-F:.]+$` 只挡得住非字符集的垃圾；"+
			"`192.168.1` / `deadbeef` / `1.2.3.4.5.6` / `:::` 全部通过那个正则，"+
			"然后在 ::inet 上抛错并打挂整条 canonical 视图的每一个读方（真库已复现）。\n"+
			"实际片段：%s", clientIPLine(proj))
	}
	if !strings.Contains(proj, "t.client_ip::inet") {
		t.Errorf("817 投影里找不到 t.client_ip::inet —— 会话分支没有消费 session_turns.client_ip")
	}

	// ② 字符类守卫必须**消失**，不能只是「再加一道」。
	//
	// **判据钉在 proj 块上而不是整个文件**：817 的头部注释为了说明 816 错在哪，
	// 正面引用了那个正则字面量。对全文做子串匹配会被**这段解释性注释**喂饱，
	// 于是「守卫还在」永远测不出来——一个被自己的说明文字触发/压制的门，
	// 跑绿了也不代表任何事。proj 块内没有注释，判据干净。
	if strings.Contains(proj, "client_ip ~") {
		t.Errorf("817 的投影里仍留着字符类守卫。\n"+
			"新旧并存说明表达式里还留着 816 的弱形态——那正是本迁移要修的东西，"+
			"留着等于没修。实际片段：%s", clientIPLine(proj))
	}
	// 同一件事在 proj 之外也**可能出现**残留（比如某个 DO 块里替 816 打的补丁），
	// 但不能写成「全文不得出现 `client_ip ~ `」——那会被 817 自己那道
	// **检查弱守卫是否残留**的探针喂饱：
	//
	//	IF position('client_ip ~ ' in v_def) > 0 THEN
	//	  RAISE EXCEPTION 'the character-class guard is still present; …'
	//
	// 这个字面量在这里是**搜索串**，不是投影表达式。子串判据分不清这两种身份，
	// 于是判据要么放过真残留、要么被自己的探针触发——两种都是坏门。
	// 正确形态：把每一处列出来，逐处判定它是不是探针。
	stripped := stripSQLLineComments816(src)
	for _, ln := range findOccurrences(stripped, "client_ip ~ ") {
		lnText := lineAt(stripped, ln)
		if strings.Contains(lnText, "position(") && strings.Contains(lnText, "in v_def") {
			continue // 探针：检查弱守卫不在场，合法
		}
		t.Errorf("817 的可执行体第 %d 行出现 `client_ip ~ `，且**不是**残留检查探针。\n"+
			"弱形态必须整体消失，不是只换掉投影那一行。该行：%s", ln, strings.TrimSpace(lnText))
	}

	// ③ 守卫的存在性判据在**渲染后**的 viewdef 上也要成立。pg_get_viewdef 会把
	// `CASE WHEN c THEN x END` 重排成 `WHEN c THEN x` + `END`（外层 CASE 字样消失，
	// 815 down 文件头部记的同款渲染差异），所以迁移里查的是 `pg_input_is_valid`
	// 这个**渲染后仍保留**的函数名，不是 'CASE WHEN'。
	if !strings.Contains(src, "position('pg_input_is_valid' in v_def)") {
		t.Errorf("817 缺少「渲染后仍存在」的守卫对账（position('pg_input_is_valid' in v_def)）。\n" +
			"迁移若不核对重建结果，视图可能压根没换成功而迁移照样绿。")
	}
	// 反向：字符类守卫的残留也必须被对账挡住（否则「只换一半」无人发现）。
	if !strings.Contains(src, "the character-class guard is still present") {
		t.Errorf("817 缺少「字符类守卫必须消失」的对账。\n" +
			"没有这条，表达式里新旧并存时迁移仍然会绿。")
	}

	// ④ 列序/列数不变：client_ip 仍在 740 的位置，没被挪走。
	//    CREATE OR REPLACE VIEW 的合法性要求列名/类型/序号全都不动。
	names := between(t, src, "names := $names$", "$names$;")
	if !strings.Contains(names, "credits_rate_multiplier, client_ip, origin_stage, token_band, client_forwarded_for") {
		t.Errorf("817 改了列序 —— client_ip 必须留在 740 的原位（列名/类型/序号都不能动）")
	}
	if !strings.HasSuffix(strings.TrimSpace(names), "origin_stage, token_band, client_forwarded_for") {
		t.Errorf("names 末尾三列次序被改动 —— 817 的作用域只有 client_ip 一行")
	}
	if !strings.Contains(src, "cnt <> 118") {
		t.Errorf("817 缺少 118 列的 fail-closed 对账")
	}
}

// TestMigration817KeepsViewChainGuard 守住我在写 817 时引入的一个回归。
//
// 816 的第一个 DO 块守的是**整条 view 链**（三个视图，680 事故形态），链不全时
// NOTICE + RETURN。而 817 改写时把它窄化成了「只查顶层视图」——但 817 的第二个
// DO 块仍然直接 regclass 了两个 wrapper 视图，并从其中一个取列清单。链不全时它
// 会崩在「relation does not exist」：那不是 no-op，是崩溃。
//
// 这道门的作用是让「链守卫」在**两个文件里都**存在，且**三个视图都**在里面。
func TestMigration817KeepsViewChainGuard(t *testing.T) {
	for _, name := range []string{
		migration817,
		strings.TrimSuffix(migration817, ".sql") + ".down.sql",
	} {
		t.Run(name, func(t *testing.T) {
			src := readMigration817(t, name)
			body := stripSQLLineComments816(src)
			// 三个视图必须都在链守卫里。少一个就等于把 680 事故形态放回来。
			for _, v := range []string{
				"request_logs_with_current_month_without_customer_id",
				"request_logs_with_current_month_without_request_class_due_at",
				"request_logs_with_current_month",
			} {
				if !strings.Contains(body, "to_regclass('public."+v+"')") {
					t.Errorf("%s 的 view 链守卫没有覆盖 %s。\n"+
						"680 事故形态：链不全时必须 NOTICE + RETURN，而不是崩在 "+
						"「relation does not exist」——后者不是 no-op。", name, v)
				}
			}
		})
	}
}

// TestMigration817DownRevertsWithWeaknessWarning 守住「回滚是有意且危险的」这件事。
func TestMigration817DownRevertsWithWeaknessWarning(t *testing.T) {
	raw := readMigration817(t, strings.TrimSuffix(migration817, ".sql")+".down.sql")
	proj := between(t, raw, "proj := $proj$", "$proj$;")

	// ① down 确实退回 816 的字符类形态。
	if !regexp.MustCompile(`CASE\s+WHEN\s+t\.client_ip\s*~\s*'\^\[0-9a-fA-F:\.\]\+\$'`).MatchString(proj) {
		t.Errorf("817 down 没有退回 816 的字符类守卫形态。\n" +
			"半吊子回滚（表达式改了但没回到 816 的确切形态）比不回滚更难排查。")
	}
	// ② down 也不能把语义守卫留着——那等于「回滚了但没回滚」。
	if strings.Contains(proj, "pg_input_is_valid") {
		t.Errorf("817 down 的投影里仍有 pg_input_is_valid —— 回滚必须真的把弱形态放回去")
	}

	// ③ **警告必须在**：回滚到一个已知会打挂全站读面的形态，是刻意的危险动作。
	//    没有警告，下一个遇到「817 让查询变慢」的人会理所当然地回滚。
	head := raw
	if i := strings.Index(raw, "BEGIN;"); i > 0 {
		head = raw[:i]
	}
	for _, kw := range []string{"危险", "回滚到它就等于把那个洞放回去", "绝不要"} {
		if !strings.Contains(head, kw) {
			t.Errorf("817 down 的头部警告缺关键词 %q。\n"+
				"回滚 817 意味着把「一个畸形 client_ip 打挂整条 canonical 视图」那个洞放回去"+
				"（审计 §9.64）。没有警告，它就是一个看起来很正常的运维动作。", kw)
		}
	}

	// ④ ledger 只能在**真的回滚了**时删。
	//    第一个 DO 块在链不全时是 NOTICE + RETURN（没回滚任何东西）；那种情况下
	//    无条件 DELETE 等于声称「817 没跑过」，而视图里还装着 817 的守卫——
	//    下次 up 会当成首次部署，而实际是已经应用过。
	if !strings.Contains(stripSQLLineComments816(raw), "DELETE FROM public.schema_migrations WHERE version = '817'") {
		t.Errorf("817 down 没有删除 ledger 行 —— 回滚后 schema_migrations 会残留一条已撤销的记录")
	}
	if !regexp.MustCompile(`(?s)keeping the ledger row`).MatchString(raw) {
		t.Errorf("817 down 的 ledger 删除没有被守卫。\n" +
			"链不全时第一个块是 no-op，此时删 ledger 会让「已应用」与「文件内容」分叉。")
	}
}

// TestMigration830DownDeletesLedgerRowSymmetricTo817 — 830 的对称性断言。
//
// 817 的 ④ 门要求 down 必须条件删除自己的台账行；830 与它同构：'830' 的
// schema_migrations 戳记由 db.ensureURSMNodeSnapshotMinDailyPartition 在 ensure
// 成功后写入（830 up 是手工迁移，本身不写台账），而 830.down 会把分区父表
// RENAME 走——回滚后台账仍留着 applied 记录无人清理，账本从此声称「830 已应用」
// 而真实 schema 已经退回普通表。本门跟随 TestMigration817DownRevertsWithWeakness
// Warning ④ 的同一判据：down 必须含条件 DELETE，且删除必须被
// 「回滚未收敛 ⇒ 保留 ledger 行」的守卫包住。
func TestMigration830DownDeletesLedgerRowSymmetricTo817(t *testing.T) {
	b, err := os.ReadFile("830_ursm_node_snapshot_min_partitioned.down.sql")
	if err != nil {
		t.Fatalf("读 830 down 失败：%v", err)
	}
	raw := string(b)
	if !strings.Contains(stripSQLLineComments816(raw), "DELETE FROM public.schema_migrations WHERE version = '830'") {
		t.Errorf("830 down 没有删除 ledger 行 —— 回滚后 schema_migrations 会残留一条已撤销的" +
			"applied 记录（戳记来自 db.ensureURSMNodeSnapshotMinDailyPartition），账本与真实 schema 分叉")
	}
	if !regexp.MustCompile(`(?s)keeping the ledger row`).MatchString(raw) {
		t.Errorf("830 down 的 ledger 删除没有被守卫。\n" +
			"回滚未收敛（父表仍是分区表）时必须保留 ledger 行并 NOTICE —— " +
			"无条件 DELETE 会把「已应用」与「文件内容」朝另一个方向分叉。")
	}
}

// TestMigration817HostileClientIPValuesDoNotBreakCanonicalView 行为门。
//
// 这是本文件里**唯一有牙**的那道：它不查文本，它往真库插敌意值再读视图。
//
// 跑法：
//
//	TEST_PG_DSN=postgres://... go test ./sql/migrations/startup/ -run TestMigration817Hostile
//
// 只读真库的既有形态；它在**事务里**跑完 817 down 来取反向证据并回滚。
func TestMigration817HostileClientIPValuesDoNotBreakCanonicalView(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()

	// 前置：视图必须**已经在 817 形态**。不是 817 形态说明这个库没装 817，
	// 此时测出来的任何东西都无意义——直接说清楚，别让它伪装成一次通过。
	var def string
	if err := pool.QueryRow(ctx,
		`SELECT pg_get_viewdef('public.request_logs_with_current_month'::regclass, true)`).Scan(&def); err != nil {
		t.Fatalf("读 canonical 视图失败：%v", err)
	}
	if !strings.Contains(def, "pg_input_is_valid") {
		t.Skip("canonical 视图还不是 817 形态（无 pg_input_is_valid）——本门只测 817 已安装的库；" +
			"请先跑 817 迁移")
	}

	// 在一个事务里造敌意值。session_turns 有若干 NOT NULL + CHECK 约束
	// （submit_mode / source_kind / quality 是枚举 CHECK，主键含 turn_no），
	// 这里按真库实测的合法取值填。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag := fmtHostileTag()
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_turns
		    (tenant_id, session_id, turn_no, request_id, ts,
		     submit_mode, source_kind, quality, partition_date, client_ip)
		SELECT $1::varchar, $1::text, g.n, $1::text || '-h' || g.n, now(),
		       'full', 'live', 'verified', current_date, g.v
		FROM unnest($2::text[]) WITH ORDINALITY AS g(v, n)`,
		tag, append(append([]string{}, hostileInvalidClientIPs...), hostileValidClientIPs...),
	); err != nil {
		t.Fatalf("插入敌意 client_ip 失败：%v", err)
	}

	// ① 正向：整条视图必须**照常返回全部行**，非法值落 NULL、合法值透传。
	rows, err := tx.Query(ctx, `
		SELECT request_id, client_ip::text
		FROM public.request_logs_with_current_month
		WHERE tenant_id = $1::text AND request_id LIKE $1::text || '-h%'
		ORDER BY request_id`, tag)
	if err != nil {
		t.Fatalf("读 canonical 视图失败（这正是 816 守卫被打挂时的症状：%v）", err)
	}
	type got struct {
		id string
		ip *string
	}
	seen := map[string]got{}
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.id, &g.ip); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		seen[g.id] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := len(hostileInvalidClientIPs) + len(hostileValidClientIPs)
	if len(seen) != want {
		t.Errorf("敌意值下视图只返回了 %d/%d 行 —— 期望全部 %d 行都在。"+
			"（行少了 = 有值把整条查询打挂了，这正是 817 要防的事）", len(seen), want, want)
	}
	for i, v := range hostileInvalidClientIPs {
		g, ok := seen[fmt.Sprintf("%s-h%d", tag, i+1)]
		if !ok {
			t.Errorf("语义非法的 client_ip %q 没有对应行返回", v)
			continue
		}
		if g.ip != nil {
			t.Errorf("client_ip=%q 在视图里投影成了 %q，期望 NULL。\n"+
				"守卫把非法值透传出来 = 读方会拿到一个它无法处理的地址。", v, *g.ip)
		}
	}
	for i, v := range hostileValidClientIPs {
		g, ok := seen[fmt.Sprintf("%s-h%d", tag, len(hostileInvalidClientIPs)+i+1)]
		if !ok {
			t.Errorf("语义合法的 client_ip %q 没有对应行返回", v)
			continue
		}
		if g.ip == nil {
			t.Errorf("client_ip=%q 合法却被投影成 NULL。\n"+
				"守卫退化成「一律落 NULL」= 修了崩溃但丢了数据（§9.18 同一形状）。", v)
		}
	}

	// ② 反向证据（**本门的关键**）：同一批输入在 816 形态下**必须报错**。
	//    没有这一步，这道门就可能是个恒过的空门——而一个恒过的门比没有门更坏，
	//    因为它会让人以为这个洞被守着。
	//
	//    用 down 迁移换回弱形态，在同一个事务里读完再整体回滚。
	downSrc := readMigration817(t, strings.TrimSuffix(migration817, ".sql")+".down.sql")
	downBody := strip817TxnWrapper(t, downSrc)
	if _, err := tx.Exec(ctx, downBody); err != nil {
		t.Fatalf("在事务里跑 817 down 失败：%v", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT count(*) FROM public.request_logs_with_current_month WHERE tenant_id = $1::text`, tag); err == nil {
		t.Errorf("反向证据不成立：在 816 形态下读 canonical 视图**没有报错**。\n" +
			"那说明这批敌意值不足以暴露弱守卫，本门测不出东西来 —— " +
			"请换更贴近真实攻击面的值（X-Real-IP 由客户端自由填写）。")
	} else if !strings.Contains(err.Error(), "invalid input syntax for type inet") {
		// **不降级成 log**：反向证据要证明的是「816 形态下这一批值会让 ::inet 抛错」。
		// 换成别的错误说明我们没证明到那件事——可能是值不够毒、也可能是别的原因
		// 把查询打死了。两种都要求人来查，所以是 error 而不是提示。
		t.Errorf("反向证据不成立：816 形态下读视图确实报错了，但**不是** inet 解析失败。\n"+
			"本门要证明的是「弱守卫放行这批值、::inet 抛错、打挂全站读面」；"+
			"换个错误说明这条因果链没被证明。实际错误：%v", err)
	} else {
		t.Logf("反向证据成立：816 形态下同一批值报 %v —— 这道门有牙", err)
	}
}

// strip817TxnWrapper 剥掉 817 迁移文件最外层的 BEGIN; / COMMIT;。
//
// 为什么必须剥：反向证据要在**一个事务里**跑 down 再整体回滚，而 down 文件自带
// BEGIN;/COMMIT;。内层 COMMIT 会把外层事务提交掉 —— 于是「回滚」根本不发生，
// 测试把真库留在 816 形态，而测试报告是绿的。
//
// 为什么对剥离结果**断言**：如果文件结构变了（比如有人给它加了 SAVEPOINT 或
// 多层嵌套），剥离会静默失配，事务保护随之消失。所以剥离必须报出实际去掉了几处
// BEGIN/COMMIT，数量不对即失败——宁可测试红，不要保护悄悄消失。
func strip817TxnWrapper(t *testing.T, src string) string {
	t.Helper()
	up := strings.Count(src, "\nBEGIN;")
	down := strings.Count(src, "\nCOMMIT;")
	if up != 1 || down != 1 {
		t.Fatalf("817 迁移的事务包装不是恰好一层 BEGIN/COMMIT（实测 BEGIN=%d COMMIT=%d），"+
			"无法安全剥壳取反向证据。**不要**为了让这个测试通过去改迁移的事务结构——"+
			"先确认文件是不是预期的形态。", up, down)
	}
	out := strings.Replace(src, "\nBEGIN;", "", 1)
	out = strings.Replace(out, "\nCOMMIT;", "", 1)
	if strings.Contains(out, "\nBEGIN;") || strings.Contains(out, "\nCOMMIT;") {
		t.Fatalf("剥壳后仍残留 BEGIN/COMMIT，事务保护不可信")
	}
	return out
}

// fmtHostileTag 造一个本次运行独有的 tenant 标记，避免撞上库里既有行。
func fmtHostileTag() string {
	return "h817-" + time.Now().UTC().Format("20060102150405.000000000")
}

// findOccurrences 返回 needle 在 src 中出现的所有 1-based 行号。
func findOccurrences(src, needle string) []int {
	var out []int
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			out = append(out, i+1)
		}
	}
	return out
}

// lineAt 取 1-based 行号对应的那一行。
func lineAt(src string, n int) string {
	lines := strings.Split(src, "\n")
	if n < 1 || n > len(lines) {
		return "<行号越界>"
	}
	return lines[n-1]
}
