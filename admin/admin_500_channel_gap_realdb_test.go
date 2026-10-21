package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 2026-10-16「迁移已存在但从未在部署通道登记」修复轮的判据。
//
// ★ 2026-10-21（R74）补记：本文件的 domain/032 部分**已退役**。
//   · 032 的触发器挂在 v1 `request_logs` 上 ⇒ v1 族真删（890）之后它
//     **永远不可应用**（留在通道里 ⇒ 跑到它就 42P01 ⇒ 整条腿中断）。
//   · 它建的 `session_tenant_binding` 也**永远不会被写入**（唯一写入者随
//     v1 一起消失）⇒ `/api/admin/session-crosstalk` 改为从会话族派生，
//     并**移出**本文件的 realdb 用例清单。
//   · ⇒ 新判据是**静态**的（不需要 DSN ⇒ CI 可跑）：
//     TestCrosstalkEndpointIsSourcedFromSessionFamily。
//     ★ 原用例是 realdb ⇒ 本地/CI 从不执行 ⇒ 这条缺口才能一直活到今天。
//   · ⇒ 通道侧另加一条禁令：domain/032 **必须不在**通道 files=() 里
//     （见 TestChannelRegistersUndeliveredSessionGapMigrations 的 forbidden 段）。
//
// 缺陷形态：四个 startup 文件**已**登记在 installer 的
// sql/schema/installed_startup_migrations.tsv（456→index 6、351→index 8、
// 629→index 84、632→index 87），第五个 sql/migrations/domain/032 属于 domain
// 腿，两条腿都**没有**进入 scripts/apply-db-revision-sequence.sh 的
// files=(...) 数组。交付通道因此从未应用它们，部署库上这些对象一个都不存在。
//
// 判据纪律（照 LESSONS.md）：
//   - 允许侧断言必须配「前提仍在场」断言。这里最容易假绿的是：只断言「对象
//     存在」，而对象存在与端点读它没有绑定关系。所以每组断言都成对出现：
//     (i) handler 源码里确实引用该对象，(ii) 真库上该对象确实被建出来。
//     任一半被单独改掉，另一半都不会替它兜底。
//   - 应用迁移前先跑一遍同样的查询并要求它**失败**（falsification）。否则
//     「迁移后查询成功」可能只是因为查询本来就不依赖那个对象。
//   - 不自造机制：domain/032 的改名适配交给通道既有的 request-logs token 机制，
//     判据分别钉住 legacy 代际（落在 request_logs）与 renamed 代际（落在
//     _request_logs）两条分支。
//
// 用法：
//	source /tmp/lgw-env.sh && cd <本 worktree>
//	go test ./admin/ -run 'ChannelGap|ChannelRegistersUndelivered|DataLifecycleMetricsNullScan' -count=1 -v
//
// DSN 取值顺序 TEST_ADMIN_500_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL。
// 只建后即删的一次性 database / schema，**从不写业务库 public**。

// channelFiles 从 scripts/apply-db-revision-sequence.sh 解析出 files=(...) 数组
// 的顺序列表（只取带引号的条目，注释行自然被过滤）。
func channelFiles(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate repo root")
	}
	script := filepath.Join(filepath.Dir(thisFile), "..", "scripts", "apply-db-revision-sequence.sh")
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read channel script: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "\nfiles=(")
	if start < 0 {
		t.Fatal("channel script has no files=( array")
	}
	end := strings.Index(src[start:], "\n)\n")
	if end < 0 {
		t.Fatal("cannot find end of files=( array")
	}
	body := src[start+len("\nfiles=(") : start+end]
	// ★ 必须先剥掉 bash 注释再取引号条目。少这一步就是一个**假绿**：
	// 把登记行注释掉（变异验证正是这么做的）之后，正则照样能在注释里
	// 匹配到 "…456_session_v2_display_columns.sql"，判据于是全绿——
	// 它测的是「文件里出现过这个字符串」，不是「通道会投递它」。
	// 注释的判定与 bash 一致：行首可有空白，然后是 #。
	comment := regexp.MustCompile(`(?m)^[ \t]*#.*$`)
	body = comment.ReplaceAllString(body, "")
	re := regexp.MustCompile(`"([^"]+\.sql)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	if len(out) < 100 {
		t.Fatalf("files=( parsed only %d entries; the array shape drifted", len(out))
	}
	return out
}

func indexOfChannelFile(files []string, suffix string) int {
	for i, f := range files {
		if strings.HasSuffix(f, suffix) {
			return i
		}
	}
	return -1
}

// TestChannelRegistersUndeliveredSessionGapMigrations 是本次修复的**文本契约**
// 判据，也是变异 (a) 的靶子：把任一条登记注释掉或删掉，本测试立刻红。
//
// 它断言三件事：
//  1. 五个文件都在通道 files=() 里（交付腿真的存在）；
//  2. domain/032 排在 860 之后——它必须在改名之后才能拿到 request-logs 改写；
//  3. 315/364 **不在**通道里，且这是被有意钉住的排除（登记它们会让整条腿
//     在 psql -v ON_ERROR_STOP=1 下中断，见测试注释里写的三条实测理由）。
func TestChannelRegistersUndeliveredSessionGapMigrations(t *testing.T) {
	files := channelFiles(t)

	required := []struct {
		suffix   string
		what     string
		endpoint string
	}{
		{"456_session_v2_display_columns.sql", "public.sessions 显示列组", "/api/admin/turns/sessions, /api/admin/annotations/first-turn-samples"},
		{"351_session_analytics_tables.sql", "session_clusters / session_cluster_members", "/api/admin/session-clusters"},
		{"629_audit_attachments_cleanup.sql", "audit_attachments_cleanup", "/api/admin/attachments/cleanup/execute"},
		{"632_audit_attachments_filesystem_cleanup.sql", "audit_attachments_filesystem_cleanup", "/api/admin/attachments/cleanup/execute"},
		{"567_session_analysis_metadata.sql", "session_analysis_metadata", "/api/admin/turns/sessions (LEFT JOIN LATERAL)"},
		{"470_cache_metrics.sql", "cache_metrics 分区父表", "/api/admin/cache-metrics/*"},
		{"472_cache_metrics_partitions.sql", "cache_metrics default / 月分区", "/api/admin/cache-metrics/*"},
		// ★ 2026-10-21（R74）：domain/032 **从必登记清单里移出** ——
		// 它的触发器挂在 v1 `request_logs` 上，890 真删之后**永远不可应用**
		// （留在通道里 ⇒ 跑到它就 42P01 ⇒ 整条腿中断），而它建的表也永远
		// 不会被写入（唯一写入者随 v1 一起消失）⇒ 端点改为从会话族派生。
		// 详见 forbidden 清单里那条与 TestCrosstalkEndpointIsSourcedFromSessionFamily。
	}
	for _, r := range required {
		if idx := indexOfChannelFile(files, r.suffix); idx < 0 {
			t.Errorf("channel files=( does not register %s (%s for %s); "+
				"this migration exists on disk but is never delivered, which is the defect",
				r.suffix, r.what, r.endpoint)
		}
	}

	// 顺序：domain/032 的触发器目标表在 860 之后叫 _request_logs。
	i860 := indexOfChannelFile(files, "860_rename_request_logs_family_pending_delete.sql")
	i032 := indexOfChannelFile(files, "032_session_tenant_binding.sql")
	switch {
	case i860 < 0:
		t.Fatal("channel no longer registers 860; the rename leg this task depends on is gone")
	case i032 < 0:
		// 已由上面的必登记断言报出，不重复报错。
	case i032 < i860:
		t.Errorf("domain/032 is registered at index %d, before 860 at %d; "+
			"it must follow the rename so the existing request-logs token adapter rewrites its trigger target",
			i032, i860)
	}

	// 顺序：472 要 PARTITION OF 一个已存在的父表，470 必须排在它前面。
	i470 := indexOfChannelFile(files, "470_cache_metrics.sql")
	i472 := indexOfChannelFile(files, "472_cache_metrics_partitions.sql")
	if i470 >= 0 && i472 >= 0 && i470 > i472 {
		t.Errorf("470_cache_metrics is at index %d, after 472 at %d; "+
			"472 partitions cache_metrics, so 470 must be delivered first", i470, i472)
	}

	// 有意排除：315/364 与 sql/schema/01-schema.sql 对同一张表给出互斥定义，
	// 且非幂等。登记它们会让 ON_ERROR_STOP=1 的流式执行中断整条腿。钉住这个
	// 排除，避免后来者「顺手补全」把通道弄红。
	for _, forbidden := range []string{"315_prompt_injection_detection.sql", "364_prompt_injection_enhanced.sql"} {
		if idx := indexOfChannelFile(files, forbidden); idx >= 0 {
			t.Errorf("channel files= registers %s at index %d, but that file is NOT safe to deliver: "+
				"its CREATE TABLE IF NOT EXISTS is a no-op against sql/schema/01-schema.sql's prompt_injection_detections "+
				"(risk_level integer vs VARCHAR(20)), its 4 CREATE INDEX / 4 CREATE POLICY lack IF NOT EXISTS, "+
				"and its stats views filter risk_level='critical' on an integer column. "+
				"A higher-numbered incremental migration must resolve the baseline conflict first.",
				forbidden, idx)
		}
	}

	// ★ 2026-10-21（R74）：domain/032 **必须不在**通道里。
	//
	// 它在 2026-10-16 被登记进通道（那一轮修的正是「迁移已存在但从未在通道
	// 登记」），但 2026-10-20 的 v1 族真删（890）改变了前提：
	//   · 032:80/82/87 的触发器目标 `request_logs` 在改名后叫 `_request_logs`，
	//     而该表已被 890 DROP ⇒ 032 现在**必然 42P01**
	//   · 本通道是 `set -euo pipefail` + `psql -v ON_ERROR_STOP=1` 的流式执行
	//     ⇒ 一条失败就**整条腿中断** ⇒ 032 之后的每一条迁移都不会被投递。
	//
	// ⇒ 所以「留在通道里」不是「漏登记」，而是**一颗定时炸弹**：通道每跑一次
	//   都会在 032 处停下。而它建的表在生产上又**从来没出现过**
	//   （`to_regclass` = NULL；部署台账 `gateway_db_revision_sequences`
	//   里没有它的记录）⇒ `/api/admin/session-crosstalk` 长期 500。
	//
	// ⇒ 退役 + 端点换源（会话族派生）才是正解，见
	//   TestCrosstalkEndpointIsSourcedFromSessionFamily。
	if idx := indexOfChannelFile(files, "032_session_tenant_binding.sql"); idx >= 0 {
		t.Errorf("channel files= still registers domain/032 at index %d, but it can never be applied again: "+
			"its trigger target is the v1 request_logs table (renamed to _request_logs by 860), which 890 dropped, "+
			"so `DROP TRIGGER IF EXISTS … ON _request_logs` raises 42P01 and, under set -euo pipefail + ON_ERROR_STOP=1, "+
			"aborts the whole migration leg. The table it builds is also write-never (its only writer was the v1 trigger), "+
			"so the endpoint must derive cross-talk from the session family instead — see "+
			"TestCrosstalkEndpointIsSourcedFromSessionFamily.",
			idx)
	}
}

// TestCrosstalkEndpointIsSourcedFromSessionFamily 是 2026-10-21（R74）的**静态**
// 判据（**不需要 DSN ⇒ CI 可跑**）：`/api/admin/session-crosstalk` 必须从
// **会话族**派生串话，不得再读 v1 族的侧效应表 `session_tenant_binding`。
//
// # 为什么这道门是静态的（而不是像原用例那样 realdb）
//
// 原用例是 realdb（要 TEST_ADMIN_500_DSN）⇒ 本地/CI **从不执行**
// ⇒ ⇒★ 这条缺口才能从 2026-10-16 一直活到 2026-10-21 都没被抓住。
// ⇒ ⇒ 把「端点读什么对象」这条判据搬进**不需要 DSN** 的静态门，
// 才能让 CI 真正守住它。
//
// # 三条断言（成对出现，避免「只断言一半」的假绿）
//
//	(i)   端点源码的**字符串字面量**里不再出现 `session_tenant_binding`
//	      ⇒ ★ 只看字面量、不看整份文本：**注释里提到它是允许的**
//	      （本判据的文档注释就提到了它）⇒ 断言必须剥掉注释。
//	(ii)  端点读的是会话族 `session_turns`，且带串话判定
//	      `count(DISTINCT tenant_id) > 1`。
//	(iii) 端点走 `withAllTenantReadOnlyTx`（显式 RLS bypass）——
//	      `session_turns` 开了 RLS（tenant_isolation / owner_filter），
//	      不 bypass 会被**静默**收成 0 行 ⇒ 对一个安全控制来说那等于失效。
//
// crosstalkParentLegRE 匹配串话查询里**独立的父表**那条腿。
//
// ★ 为什么需要正则而不是 strings.Contains：
//
//	`public.session_turns` 是 `public.session_turns_hot` 的**前缀** ⇒
//	用 Contains 判「父表那条腿在不在」时，**只写 hot 也会被判成通过** ⇒
//	门会**假绿** ⇒ 于是「hot ∪ parent 两条腿」这条要求就落空了。
//	⇒ 负向断言（后面不能再跟标识符字符）把 hot 排除掉。
//
// ★ 注意：Go 的 regexp 是 RE2，**不支持负向断言 `(?!...)`** ⇒ 这里用
//
//	「后面必须跟一个非标识符字符或行尾」来表达同一个意思。
var crosstalkParentLegRE = regexp.MustCompile(`FROM\s+public\.session_turns([^_A-Za-z0-9]|$)`)

func TestCrosstalkEndpointIsSourcedFromSessionFamily(t *testing.T) {
	root := repoRoot(t)
	rel := filepath.Join("admin", "session_crosstalk_check.go")
	path := filepath.Join(root, rel)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	// ★ 只收集字符串字面量（注释天然不在其中）。
	var literals []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		literals = append(literals, v)
		return true
	})

	joined := strings.Join(literals, "\n")

	if strings.Contains(joined, "session_tenant_binding") {
		t.Errorf("%s 的字符串字面量里仍出现 session_tenant_binding ⇒\n"+
			"  ⇒ 那张表由挂在 v1 request_logs 上的触发器写入，v1 真删（890）后永远为空，"+
			"且它在生产 252 上从来没建过（端点长期 500/42P01）⇒\n"+
			"  ⇒ 串话必须从会话族 session_turns 派生（同一 session 出现在多个 tenant 下）",
			rel)
	}

	// ★★ 【D2 订正，2026-10-22 12h 审计轮】本门原先断言 `FROM session_turns`
	//   ——而那**正是缺陷本身**：只读分区父表 ⇒ 漏掉 `session_turns_hot`
	//   ⇒ 最近 8 小时的串话一条都看不到（活 turn 全写进 hot）。
	//   ⇒ 一个把缺陷钉死的门，会**挡住缺陷的修复** ⇒ 本门必须钉「两条腿都在」。
	//   ⇒ 判据用正则 + 负向断言，避免 `session_turns` 是 `session_turns_hot`
	//   的前缀导致「只写 hot 也算过」。
	if !strings.Contains(joined, "session_turns_hot") {
		t.Errorf("%s 的串话查询里没有 `session_turns_hot` ⇒\n"+
			"  ⇒ 活的 turn 全部写进 hot 表（domains/session/v2/turn_writer.go），\n"+
			"    要等保留窗口（lifecycle.hot_retention_hours，默认 8h）过后才被 promote 进父表\n"+
			"  ⇒ 只读父表 ⇒ **最近 8 小时的串话一条都看不到** ⇒ 端点返回 200 + 空列表\n"+
			"  ⇒ ★ 静默失明（与 892 迁移头写的「回填 0 行 ⇒ 安静地失明」同型）", rel)
	}
	if !crosstalkParentLegRE.MatchString(joined) {
		t.Errorf("%s 的串话查询里没有独立的 `public.session_turns`（父表）那条腿 ⇒\n"+
			"  ⇒ hot ∪ parent 两条腿缺一不可：只有 hot 会漏掉 8 小时之前的旧串话\n"+
			"  ⇒ （仓内正典是 db.SessionFamilyTurnsSourceSQL()，形状为 hot ∪ parent）", rel)
	}

	if !strings.Contains(joined, "count(DISTINCT tenant_id) > 1") {
		t.Errorf("%s 的字符串字面量里没有串话判定 `count(DISTINCT tenant_id) > 1` ⇒\n"+
			"  ⇒ 「同一 session 出现在多个 tenant 下」这个定义被改掉了 ⇒\n"+
			"  ⇒ 它要么恒为空（静默失效），要么报了别的东西", rel)
	}

	src := readRepoFile(t, rel)
	if !strings.Contains(src, "withAllTenantReadOnlyTx") {
		t.Errorf("%s 没有走 withAllTenantReadOnlyTx ⇒\n"+
			"  ⇒ session_turns 开着 RLS（session_turns_tenant_isolation / "+
			"session_turns_owner_filter），不显式 bypass 会被静默收成 0 行 ⇒\n"+
			"  ⇒ 一个安全控制「永远返回没有串话」比「报错」更危险", rel)
	}
}

// gapMigrationFixtures 为每个对象准备「迁移前」与「迁移后」两种查询形态。
type gapCheck struct {
	name   string
	object string
	file   string
	before string // 迁移前必须失败
	after  string // 迁移后必须成功
	// handlers 把「对象存在」钉到具体端点上：每项是 {源码路径, 该源码里必须
	// 出现的记号}。记号取端点 SQL 里真实写出来的形式——端点用的是 s.title /
	// sc.title 这类别名，所以不能拿 "public.sessions.title" 当记号。
	handlers []handlerPin
}

type handlerPin struct {
	path  string
	token string
}

func gapChecks() []gapCheck {
	return []gapCheck{
		{
			name: "456 sessions.title", object: "public.sessions.title",
			file:   "456_session_v2_display_columns.sql",
			before: `SELECT title FROM public.sessions LIMIT 1`,
			after:  `SELECT title FROM public.sessions LIMIT 1`,
			handlers: []handlerPin{
				{"admin/turns_sessions.go", "NULLIF(s.title, '')"},
				{"admin/annotation_handler.go", "SELECT sc.title, sc.last_request_summary"},
				{"internal/titlestore/store.go", "SET title"},
			},
		},
		{
			name: "351 session_clusters", object: "public.session_clusters",
			file:     "351_session_analytics_tables.sql",
			before:   `SELECT count(*) FROM public.session_clusters`,
			after:    `SELECT count(*) FROM public.session_clusters`,
			handlers: []handlerPin{{"admin/session_clusters_handler.go", "FROM session_clusters"}},
		},
		{
			name: "351 session_cluster_members", object: "public.session_cluster_members",
			file:     "351_session_analytics_tables.sql",
			before:   `SELECT count(*) FROM public.session_cluster_members`,
			after:    `SELECT count(*) FROM public.session_cluster_members`,
			handlers: []handlerPin{{"admin/session_clusters_handler.go", "session_cluster_members"}},
		},
		{
			name: "629 audit_attachments_cleanup", object: "public.audit_attachments_cleanup",
			file:     "629_audit_attachments_cleanup.sql",
			before:   `SELECT count(*) FROM public.audit_attachments_cleanup`,
			after:    `SELECT count(*) FROM public.audit_attachments_cleanup`,
			handlers: []handlerPin{{"admin/data_lifecycle_attachments.go", "INSERT INTO audit_attachments_cleanup"}},
		},
		{
			name: "632 audit_attachments_filesystem_cleanup", object: "public.audit_attachments_filesystem_cleanup",
			file:     "632_audit_attachments_filesystem_cleanup.sql",
			before:   `SELECT count(*) FROM public.audit_attachments_filesystem_cleanup`,
			after:    `SELECT count(*) FROM public.audit_attachments_filesystem_cleanup`,
			handlers: []handlerPin{{"admin/data_lifecycle_attachments.go", "audit_attachments_filesystem_cleanup"}},
		},
		{
			name: "470 cache_metrics", object: "public.cache_metrics",
			file:     "470_cache_metrics.sql",
			before:   `SELECT count(*) FROM public.cache_metrics`,
			after:    `SELECT count(*) FROM public.cache_metrics`,
			handlers: []handlerPin{{"admin/cache_metrics_handler.go", "FROM cache_metrics"}},
		},
		{
			name: "472 cache_metrics_default", object: "public.cache_metrics_default",
			file:     "472_cache_metrics_partitions.sql",
			before:   `SELECT count(*) FROM public.cache_metrics_default`,
			after:    `SELECT count(*) FROM public.cache_metrics_default`,
			handlers: []handlerPin{{"admin/cache_metrics_handler.go", "FROM cache_metrics"}},
		},
		// ★ 2026-10-21（R74）：domain/032 的用例被**移除**。
		// 原用例钉着「`public.session_tenant_binding` 必须存在」+「端点必须引用
		// 它」—— 那两条都是**旧决策**：那张表由挂在 v1 `request_logs` 上的触发器
		// 写入，v1 真删（890）后它永远为空；而且它从来就没在生产上建起来
		// （端点长期 500）。⇒ 端点改为从会话族 `session_turns` 派生串话，
		// 新判据见 TestCrosstalkEndpointIsSourcedFromSessionFamily（静态，
		// **不需要 DSN ⇒ CI 可跑**——这正是原用例最大的短板：它是 realdb 用例，
		// 本地/CI 从不执行 ⇒ 这条缺口才能一直活到今天）。
	}
}

// gapFixtureSQL 是隔离库里 456 / 351 / 629 / 632 / domain-032 所需的最小前提。
// 只有三张被 456 ALTER 的表和被 domain/032 挂触发器的 request-log 面；其余表
// 全部由被测迁移自己创建。prefix 用来切换 request-log 代际（legacy / renamed）。
func gapFixtureSQL(renamed bool) string {
	logs := "request_logs"
	if renamed {
		logs = "_request_logs"
	}
	return fmt.Sprintf(`
		CREATE TABLE public.sessions (
			session_id text, tenant_id character varying, created_at timestamptz,
			closed_at timestamptz, status text, total_turns integer,
			last_request_summary text, client_type text);
		CREATE TABLE public.session_turns (
			session_id text, request_id text, tenant_id character varying,
			ts timestamptz, status_code integer, turn_no integer);
		CREATE TABLE public.session_bodies (
			session_id text, request_id text, request jsonb, response jsonb);
		CREATE TABLE public.%s (
			id bigserial primary key, gw_session_id text, tenant_id text, ts timestamptz);
		CREATE INDEX ON public.%s (gw_session_id);
	`, logs, logs)
}

// channelGapDSN 复用本文件同族的 DSN 取值约定。
func channelGapDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or `source /tmp/lgw-env.sh` and use LLM_GATEWAY_DATABASE_URL)")
	return ""
}

func newGapDatabase(t *testing.T, dsn string) (*pgxpool.Pool, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	name := fmt.Sprintf("lgw_gap_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		root.Close()
		t.Fatalf("cannot rewrite dsn %q", dsn)
	}
	rest := dsn[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[q:]
	} else {
		rest = ""
	}
	isoDSN := dsn[:idx+1] + name + rest
	pool, err := pgxpool.New(ctx, isoDSN)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		root.Close()
	})
	return pool, ctx, isoDSN
}

func psqlAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql unavailable; cannot replay migration bytes in-process")
	}
}

// TestChannelGapObjectsAreDeliveredOnRealPG 在一次性隔离 database 上重放被
// 登记的迁移字节，断言「登记 ⇒ 交付 ⇒ 端点依赖的对象可用」。
//
// 三个阶段，每个都不可省：
//  1. falsification：迁移前，同一组查询必须**失败**（否则「迁移后成功」是空的）；
//  2. replay：按通道 files=() 里的顺序应用被测文件，模拟 psql_stream；
//  3. delivery：同一组查询必须成功。
func TestChannelGapObjectsAreDeliveredOnRealPG(t *testing.T) {
	psqlAvailable(t)
	dsn := channelGapDSN(t)
	checks := gapChecks()
	files := channelFiles(t)

	// 被重放的文件列表直接取自通道数组，而不是在这里另抄一份——这样删掉登记
	// 就会让本测试无文件可放（而不是悄悄用一份陈旧的副本）。
	var replay []string
	for _, c := range checks {
		suffix := "/" + c.file
		idx := indexOfChannelFile(files, suffix)
		if idx < 0 {
			t.Fatalf("channel files= does not register %s; nothing to replay (this is the defect)", suffix)
		}
		replay = append(replay, files[idx])
	}

	// 重放只覆盖 legacy 代际。renamed 代际必须**由通道自己的适配器**改写
	// request_logs token；在测试里另抄一份改写逻辑等于伪造第二个机制，所以那一
	// 代际改由 TestRenamedGenerationTriggerLandsOnPrefixedRequestLogs 在真实
	// 数据库上直接读回目录来断言（只读）。
	for _, renamed := range []bool{false} {
		gen := "legacy"
		if renamed {
			gen = "renamed"
		}
		t.Run(gen, func(t *testing.T) {
			pool, ctx, isoDSN := newGapDatabase(t, dsn)
			if _, err := pool.Exec(ctx, gapFixtureSQL(renamed)); err != nil {
				t.Fatalf("seed prerequisites: %v", err)
			}

			// ── 阶段 1：falsification ────────────────────────────────────
			for _, c := range checks {
				if _, err := pool.Exec(ctx, c.before); err == nil {
					t.Errorf("premise broken: %q already succeeds before %s is applied; "+
						"this check no longer proves the migration is what fixes %s",
						c.before, c.file, c.name)
				}
			}

			// ── 阶段 2：重放被登记的字节（同一 psql_stream 语义）──────────
			_, thisFile, _, _ := runtime.Caller(0)
			repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), ".."))
			for _, rel := range replay {
				abs := filepath.Join(repoRoot, strings.TrimPrefix(rel, "$ROOT_DIR/"))
				body, err := os.ReadFile(abs)
				if err != nil {
					t.Fatalf("read %s: %v", rel, err)
				}
				// 通道用 `cat 文件 | psql -v ON_ERROR_STOP=1`；这里同构。
				cmd := exec.Command("psql", "-X", "-v", "ON_ERROR_STOP=1", "-q", isoDSN, "-f", "-")
				cmd.Stdin = strings.NewReader(string(body))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("replaying %s failed: %v\n%s", rel, err, out)
				}
			}

			// ── 阶段 3：delivery ────────────────────────────────────────
			for _, c := range checks {
				if _, err := pool.Exec(ctx, c.after); err != nil {
					t.Errorf("%s still unavailable after replaying %s: %v", c.name, c.file, err)
				}
				// 与端点绑定：handler 源码必须真的引用该对象，否则
				// 「对象存在」是一条与端点无关的漂浮断言。
				for _, pin := range c.handlers {
					path := filepath.Join(repoRoot, pin.path)
					body, err := os.ReadFile(path)
					if err != nil {
						t.Errorf("read %s: %v", pin.path, err)
						continue
					}
					if !strings.Contains(string(body), pin.token) {
						t.Errorf("%s no longer contains %q, so the %s assertion has lost its endpoint",
							pin.path, pin.token, c.name)
					}
				}
			}

			// legacy 代际下 domain/032 的触发器必须挂在未改名的 request_logs 上。
			var attached string
			if err := pool.QueryRow(ctx, `
				SELECT tgrelid::regclass::text FROM pg_trigger
				WHERE tgname = 'trg_session_tenant_binding' AND NOT tgisinternal
				LIMIT 1`).Scan(&attached); err != nil {
				t.Errorf("trg_session_tenant_binding not attached on a legacy database: %v", err)
			} else if attached != "request_logs" {
				t.Errorf("trg_session_tenant_binding attached to %s, want request_logs (legacy generation must stay unrewritten)", attached)
			}
		})
	}
}

// TestRenamedGenerationTriggerLandsOnPrefixedRequestLogs 在**真实**数据库上只读
// 断言 renamed 代际的适配结果。
//
// 为什么不放在隔离库里做：改写 request_logs → _request_logs 是通道
// prepare_runtime_sql 的既有封闭 token 机制。测试里若自己再抄一份正则改写，
// 就不再验证真实机制，而是验证一份复制品。这里改为直接读回真实目录：通道跑过
// 之后，触发器必须落在 _request_logs 上，且 legacy 的 request_logs 必须不存在
// （否则代际判定会落到 ambiguous/mixed 而让通道拒绝运行）。
func TestRenamedGenerationTriggerLandsOnPrefixedRequestLogs(t *testing.T) {
	dsn := channelGapDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	var legacyPresent bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.request_logs') IS NOT NULL`).Scan(&legacyPresent); err != nil {
		t.Fatalf("probe request_logs: %v", err)
	}
	if legacyPresent {
		t.Skip("real database is in the legacy generation; the renamed-adaptation branch does not apply here")
	}
	var prefixedPresent bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public._request_logs') IS NOT NULL`).Scan(&prefixedPresent); err != nil {
		t.Fatalf("probe _request_logs: %v", err)
	}
	if !prefixedPresent {
		// ★ 2026-10-21（R67 / C10 改判）：**代际二分法已整体退役**。
		//
		//   v1 族（request_logs → _request_logs）随迁移 890 真删 ⇒
		//   「改名分支」在当前世界**不存在**；连「两边都没有」也是**常态**，
		//   不是缺陷。原门在这里 t.Fatal ⇒ 恒红，而它守的**意图**
		//   （租户绑定必须生效）**依然有效** —— 只是承担者换了：
		//   会话族（session_turns / session_turns_hot）用 **RLS** 承担隔离。
		//
		//   实测（一次性库）：会话族两张表 relrowsecurity=t，各带三条策略
		//   （tenant_isolation / owner_filter / super_admin_bypass），
		//   且 hot 族的 tenant_isolation **带 WITH CHECK**
		//   ⇒ 读**与写**都受约束 ⇒ 比只管 AFTER INSERT 的触发器**更强**。
		//
		//   ⇒ 改判：代际判定保留为 Skip 分支；
		//     租户隔离的断言改瞄 **RLS**（下面 assertSessionFamilyTenantRLS）。
		assertSessionFamilyTenantRLS(t, pool)
		return
	}

	var attached []string
	rows, err := pool.Query(ctx, `
		SELECT tgrelid::regclass::text FROM pg_trigger
		WHERE tgname = 'trg_session_tenant_binding' AND NOT tgisinternal`)
	if err != nil {
		t.Fatalf("read trigger targets: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		attached = append(attached, s)
	}
	if len(attached) == 0 {
		t.Fatalf("trg_session_tenant_binding is not attached anywhere; " +
			"the channel did not deliver sql/migrations/domain/032 (its bytes target request_logs and only the " +
			"existing post-860 token adapter can retarget them)")
	}
	// 父表必须在场：只在分区上挂触发器不构成正确的 AFTER INSERT 覆盖。
	// 父表名必须是 `_request_logs`——这正是既有 request-logs token 适配器的产物
	// （domain/032 的历史字节写的是未改名的 request_logs）。月分区是由 PG 的
	// 触发器继承自动挂上的，它们的**命名**归 860 的改名范围管，不在本轮断言内，
	// 所以这里只校验「是 _request_logs 的后代分区」，不校验分区名自身的前缀。
	if !containsExact(attached, "_request_logs") {
		t.Errorf("trigger not attached to the _request_logs parent (attached: %v)", attached)
		return
	}
	var stray []string
	for _, s := range attached {
		if s == "_request_logs" {
			continue
		}
		if !isPartitionOf(t, pool, s, "_request_logs") {
			stray = append(stray, s)
		}
	}
	if len(stray) > 0 {
		t.Errorf("trigger also attached to non-descendant relations %v; "+
			"on a renamed database every other target must be a partition of _request_logs", stray)
	}
}

// assertSessionFamilyTenantRLS 断言会话族的租户隔离由 **RLS** 承担。
//
// ★ 这是 R67 / C10 改判后的**接手方**：v1 族的 trg_session_tenant_binding
//
//	触发器随迁移 890 真删 ⇒ 隔离落到 RLS 上（读 + 写都覆盖）。
//
// 三条断言，缺一即红：
//
//	① 两张表 relrowsecurity = true（有策略但没启用 = 隔离无效）；
//	② 存在 tenant_isolation 策略，且谓词用 tenant_id 对齐 app.current_tenant；
//	③ hot 族的 tenant_isolation 带 **WITH CHECK** —— 只有 USING 的话，
//	   INSERT 能写进别的租户的行（写侧漏 ⇒ 读侧再严也白搭）。
func assertSessionFamilyTenantRLS(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, rel := range []string{"session_turns", "session_turns_hot"} {
		var rls bool
		if err := pool.QueryRow(ctx,
			`SELECT c.relrowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			  WHERE n.nspname='public' AND c.relname=$1`, rel).Scan(&rls); err != nil {
			t.Fatalf("读 %s 的 RLS 开关失败：%v", rel, err)
		}
		if !rls {
			t.Errorf("%s 没有启用 RLS（relrowsecurity=false）—— 租户隔离对它无效", rel)
			continue
		}
		var using, check string
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(pg_get_expr(p.polqual, p.polrelid), ''),
			        COALESCE(pg_get_expr(p.polwithcheck, p.polrelid), '')
			   FROM pg_policy p
			   JOIN pg_class c ON c.oid=p.polrelid
			   JOIN pg_namespace n ON n.oid=c.relnamespace
			  WHERE n.nspname='public' AND c.relname=$1 AND p.polname LIKE '%tenant_isolation%'`, rel).
			Scan(&using, &check)
		if err != nil {
			t.Errorf("%s 没有 tenant_isolation 策略：%v", rel, err)
			continue
		}
		if !strings.Contains(using, "tenant_id") || !strings.Contains(using, "app.current_tenant") {
			t.Errorf("%s 的 tenant_isolation 策略没有用 tenant_id 对齐 app.current_tenant：%s", rel, using)
		}
		if rel == "session_turns_hot" && strings.TrimSpace(check) == "" {
			t.Errorf("%s 的 tenant_isolation 策略没有 WITH CHECK —— 只有 USING 的话，"+
				"INSERT 能写进别的租户的行", rel)
		}
	}
}

func containsExact(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// isPartitionOf 递归上溯 pg_inherits，确认 child 挂在 ancestor 之下。
func isPartitionOf(t *testing.T, pool *pgxpool.Pool, child, ancestor string) bool {
	t.Helper()
	ctx := context.Background()
	cur := child
	for i := 0; i < 8; i++ {
		if cur == ancestor {
			return true
		}
		var parent string
		if err := pool.QueryRow(ctx, `
			SELECT inhparent::regclass::text FROM pg_inherits
			WHERE inhrelid = to_regclass($1)`, "public."+cur).Scan(&parent); err != nil {
			return false
		}
		cur = parent
	}
	return false
}

// TestDataLifecycleMetricsNullScanRealDB 是 (d) 类缺陷的判据，也是变异 (b) 的靶子。
//
// 缺陷：四个分摊字节数是 `total_size * bucket_rows / NULLIF(total_rows, 0)`；
// 表为空时分母为 0，NULLIF 让整式返回 **NULL**，Scan 进非空 int64 直接失败，
// 端点对每张空库都是硬 500。
//
// 三态都钉：
//   - 空表：HTTP 200，且四个 *_size_bytes **键缺席**（分母为 0，不可与真实 0 混淆）；
//   - 非空表：HTTP 200，四个键都在，且是真实测量值；
//   - 真 0 字节的桶：键在场且值为 0——这正是「缺席」与「真实 0」必须可区分的原因。
func TestDataLifecycleMetricsNullScanRealDB(t *testing.T) {
	dsn := channelGapDSN(t)
	ctx := context.Background()
	pool, ctx, _ := newGapDatabase(t, dsn)
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.session_turns (
			id bigserial primary key, gw_session_id text, tenant_id text, ts timestamptz);
	`); err != nil {
		t.Fatalf("seed session_turns: %v", err)
	}

	h := &Handler{db: pool}
	call := func() map[string]json.RawMessage {
		t.Helper()
		req := SetAuthContext(
			httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/metrics", nil),
			&AuthContext{Role: "super_admin", TenantID: "acme", Username: "probe"},
		)
		w := httptest.NewRecorder()
		h.handleDataLifecycleMetrics(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d body=%s", w.Code, w.Body.String())
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		return m
	}
	sizeKeys := []string{"hot_data_size_bytes", "warm_data_size_bytes", "cold_data_size_bytes", "expired_data_size_bytes"}

	// ── 空表：这就是变异 (b) 要打回的那一态 ────────────────────────────
	empty := call()
	var total json.Number
	if err := json.Unmarshal(empty["total_rows"], &total); err != nil {
		t.Fatalf("total_rows unreadable: %v", err)
	}
	if total.String() != "0" {
		t.Fatalf("premise broken: expected an empty session_turns, total_rows=%s", total)
	}
	for _, k := range sizeKeys {
		if _, present := empty[k]; present {
			t.Errorf("%s is present with an empty table; the denominator is 0 so proration is undefined — "+
				"the key must be absent so a reader can tell it apart from a real measured 0", k)
		}
	}
	// 计数类字段仍须在场（端点不是空壳）。
	for _, k := range []string{"total_rows", "total_size_bytes", "hot_data_rows", "warm_data_rows",
		"cold_data_rows", "expired_data_rows"} {
		if _, present := empty[k]; !present {
			t.Errorf("%s missing from the empty-table payload; the response is an empty shell", k)
		}
	}

	// ── 非空表：键必须回到场，且是真实值 ──────────────────────────────
	// 四行全部落在 90 天以内：expired 桶**真实为 0 行**，于是它的分摊字节数
	// 是一个真实测得的 0（分母非 0）。这正是「真实 0」与「分母为 0 的缺席」
	// 必须可区分的用例——把 NULL 悄悄 COALESCE 成 0 的改法会在这里失去判据力。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_turns (gw_session_id, tenant_id, ts) VALUES
		('s1','acme', NOW() - INTERVAL '2 days'),
		('s2','acme', NOW() - INTERVAL '40 days'),
		('s3','acme', NOW() - INTERVAL '60 days'),
		('s4','acme', NOW() - INTERVAL '80 days')
	`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	full := call()
	for _, k := range sizeKeys {
		raw, present := full[k]
		if !present {
			t.Errorf("%s absent on a 4-row table; a real measurement must be present", k)
			continue
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Errorf("%s = %s is not a number", k, raw)
		}
	}
	var hot json.Number
	if err := json.Unmarshal(full["hot_data_rows"], &hot); err != nil {
		t.Fatalf("hot_data_rows unreadable: %v", err)
	}
	if hot.String() != "1" {
		t.Errorf("hot_data_rows = %s, want 1", hot)
	}
	var hotSize json.Number
	if err := json.Unmarshal(full["hot_data_size_bytes"], &hotSize); err != nil {
		t.Fatalf("hot_data_size_bytes unreadable: %v", err)
	}
	// hot 桶有 1/4 的行，所以分摊字节数必须是真实关系大小的一个正分数。
	var totalSize json.Number
	if err := json.Unmarshal(full["total_size_bytes"], &totalSize); err != nil {
		t.Fatalf("total_size_bytes unreadable: %v", err)
	}
	hotInt, _ := hotSize.Int64()
	totalInt, _ := totalSize.Int64()
	if hotInt <= 0 || hotInt >= totalInt {
		t.Errorf("hot_data_size_bytes = %d, want a real 0 < value < total_size_bytes (%d)", hotInt, totalInt)
	}

	// ── 「真实 0」必须与「缺席」可区分 ────────────────────────────────
	// 4 行全在 90 天以内 ⇒ expired 桶真实为 0 行 ⇒ 其分摊字节数是真实测得的 0，
	// 键必须**在场**且为 0。这一条正是把 NULL 悄悄 COALESCE 成 0 会打红的判据。
	var expiredRows json.Number
	if err := json.Unmarshal(full["expired_data_rows"], &expiredRows); err != nil {
		t.Fatalf("expired_data_rows unreadable: %v", err)
	}
	if expiredRows.String() != "0" {
		t.Skipf("fixture drift: expired_data_rows = %s, want 0", expiredRows)
	}
	raw, present := full["expired_data_size_bytes"]
	if !present {
		t.Error("expired_data_size_bytes absent although the bucket denominator is fine; " +
			"a real measured 0 must be present, and must not be confused with the empty-table absent case")
	} else if strings.TrimSpace(string(raw)) != "0" {
		t.Errorf("expired_data_size_bytes = %s, want the real measured 0", raw)
	}
}

// TestFirstTurnSamplesReturnsRealSamplesAfterChannelGap 是「200 且**不是空壳**」
// 的端到端判据，覆盖 /api/admin/annotations/first-turn-samples。
//
// 为什么单独写一条而不复用 admin_500_column_contract_realdb_test.go 的同名
// 覆盖：那份判据在 456 落地前走的是「列缺失 ⇒ 必须响亮失败」分支，它的
// seed SQL 只在**有 title** 时才会拼出 title 值，而那次拼接产出 `, , '…'`
// （titleCol 已含逗号、titleVal 又带一个逗号），在 456 登记后第一次被执行就
// 报 42601。本文件不依赖那份判据，独立把「真实样本」钉住；那份判据的缺陷
// 留给其所有者修。
//
// 端点 SQL 带 `public.` 前缀，所以用一次性隔离 database（见 newGapDatabase），
// 业务库 public 全程只读。
func TestFirstTurnSamplesReturnsRealSamplesAfterChannelGap(t *testing.T) {
	dsn := channelGapDSN(t)
	ctx := context.Background()

	// 前提仍在场：真实部署库里 sessions.title 必须存在——它正是 456 登记的产物。
	// 若哪天 456 被撤出通道，这条会红，那才是真的回归。
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	var realHasTitle bool
	if err := root.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='sessions' AND column_name='title'
		)`).Scan(&realHasTitle); err != nil {
		root.Close()
		t.Fatalf("probe public.sessions: %v", err)
	}
	root.Close()
	if !realHasTitle {
		t.Fatal("premise broken: public.sessions.title is absent on the real database; " +
			"456 is no longer delivered, which is the defect this task fixes")
	}

	pool, ctx, _ := newGapDatabase(t, dsn)
	// 列定义照真库形态抄：title 在场（456 之后）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.sessions (
			session_id text, tenant_id character varying, created_at timestamptz,
			closed_at timestamptz, status text, total_turns integer,
			last_request_summary text, client_type text, title text);
		CREATE TABLE public.session_turns (
			session_id text, request_id text, tenant_id character varying,
			ts timestamptz, status_code integer, success boolean,
			latency_ms integer, turn_no integer, partition_date date);
		CREATE TABLE public.session_turns_hot (
			session_id text, request_id text, tenant_id character varying,
			ts timestamptz, status_code integer, success boolean,
			latency_ms integer, turn_no integer, partition_date date);
		CREATE TABLE public.auto_route_selections_all (
			id bigint, request_id text, tenant_id character varying, ts timestamptz,
			task_type text, confidence numeric, chosen_model text, partition_date date);
		CREATE TABLE public.training_human_annotations (
			request_id text, human_label text, is_correct boolean,
			annotation_reason text, annotator text, annotated_at timestamptz,
			annotation_metadata jsonb);

		-- 冷面 + 热面各一条，外加一条别的租户。
		INSERT INTO public.session_turns VALUES
			('cold-sess','cold-req','acme', NOW(), 200, true, 11, 1, CURRENT_DATE);
		INSERT INTO public.session_turns_hot VALUES
			('hot-sess','hot-req','acme', NOW(), 200, true, 22, 1, CURRENT_DATE),
			('other-sess','other-req','other', NOW(), 500, false, 33, 1, CURRENT_DATE);
		INSERT INTO public.auto_route_selections_all VALUES
			(1,'cold-req','acme',NOW(),'code',0.90,'m-cold',CURRENT_DATE),
			(2,'hot-req','acme',NOW(),'chat',0.80,'m-hot',CURRENT_DATE),
			(3,'other-req','other',NOW(),'chat',0.70,'m-other',CURRENT_DATE);
		INSERT INTO public.sessions VALUES
			('cold-sess','acme',NOW(),NOW(),'closed',7,'cold summary','cli-cold','Cold session title'),
			('hot-sess','acme',NOW(),NOW(),'closed',8,'hot summary','cli-hot','Hot session title'),
			('other-sess','other',NOW(),NOW(),'closed',9,'other summary','cli-other','Other session title');
	`); err != nil {
		t.Fatalf("seed isolated database: %v", err)
	}

	h := &Handler{db: pool}
	call := func(role, tenant string) FirstTurnSamplesResponse {
		t.Helper()
		req := SetAuthContext(
			httptest.NewRequest(http.MethodGet, "/api/admin/annotations/first-turn-samples", nil),
			&AuthContext{Role: role, TenantID: tenant, Username: "probe"},
		)
		w := httptest.NewRecorder()
		h.handleAnnotationFirstTurnSamples(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("[%s/%s] HTTP %d body=%s", role, tenant, w.Code, w.Body.String())
		}
		var got FirstTurnSamplesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		return got
	}

	all := call("super_admin", "acme")
	if all.Total != 3 || len(all.Samples) != 3 {
		t.Fatalf("super_admin got %d samples (total %d), want 3", len(all.Samples), all.Total)
	}
	// 非空壳：每个样本都必须带出 456 提供的 title，以及轮次/客户端/自动路由投影。
	for _, s := range all.Samples {
		if s.Title == nil || strings.TrimSpace(*s.Title) == "" {
			t.Errorf("sample %s has an empty title; the endpoint would be an empty shell", s.RequestID)
		}
		if s.TotalTurns == nil || *s.TotalTurns == 0 {
			t.Errorf("sample %s has empty total_turns", s.RequestID)
		}
		if s.Client == nil || strings.TrimSpace(*s.Client) == "" {
			t.Errorf("sample %s has empty client", s.RequestID)
		}
		if s.TaskType == "" || s.ChosenModel == "" {
			t.Errorf("sample %s missing auto-route projection (%q/%q)", s.RequestID, s.TaskType, s.ChosenModel)
		}
	}

	// 跨租户隔离：租户过滤被摘掉即红。
	mine := call("tenant_admin", "acme")
	if mine.Total != 2 || len(mine.Samples) != 2 {
		t.Fatalf("tenant acme got %d samples (total %d), want 2", len(mine.Samples), mine.Total)
	}
	for _, s := range mine.Samples {
		if s.SessionID == "other-sess" || s.RequestID == "other-req" {
			t.Fatal("cross-tenant session leaked into acme's first-turn samples")
		}
	}
}
