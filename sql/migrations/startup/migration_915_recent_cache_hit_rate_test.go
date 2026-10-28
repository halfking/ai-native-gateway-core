package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// migration_915_recent_cache_hit_rate_test.go —— 915（P1-B-③
// recent_cache_hit_rate）的契约门。
//
// # 这道门防什么（2026-10-28 211 routing_schema_error 事故的三层教训）
//
//	① 部署序列漏登记 ⇒ 函数从未建 ⇒ 候选查询 42883 ⇒ 100% 流量 500；
//	② 首版迁移体读的是已随 890 退役的 request_logs_hot（旧列名
//	   outbound_model/client_model 也不在会话族里）；
//	③ 迁移体与运行时 SSOT（db.recentCacheHitRateEnsureSQL）若漂移 ⇒
//	   「迁移装一个体、boot 自愈换另一个体」，每次重启互相抹掉。
//
// 三层各有判据：
//	· ② 由「负向判据：可执行文本里不得出现 v1 族表/列名」钉住（先剥注释，
//	  文件头的追记注释按名提及 request_logs_hot 是合法的史实记载）；
//	· ③ 由「迁移函数体 ⇔ db SSOT 归一化文本相等」钉住（890 对 rsr 的同款）；
//	· ① 由「三条投递通道（installer embeddata / TSV / 部署序列 files 数组）
//	  都必须登记本文件」钉住。
//
// 判据纪律（本门自身，随 890 门）：阳性对照先行——检测器先证明自己抓得到
// 坏样本，负向断言才有牙齿。

const mig915File = "915_recent_cache_hit_rate.sql"

// normalizeSQL915 归一化 SQL 文本：去行注释、压空白（与 890 门的
// normalizeSQL885 同形；各文件自持一份，不跨文件导出口径）。
func normalizeSQL915(s string) string {
	lineComment := regexp.MustCompile(`--[^\n]*`)
	s = lineComment.ReplaceAllString(s, " ")
	ws := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(ws.ReplaceAllString(s, " "))
}

// stripComments915 只剥注释、不压空白（负向判据用：保留可执行文本原貌）。
func stripComments915(s string) string {
	lineComment := regexp.MustCompile(`--[^\n]*`)
	return lineComment.ReplaceAllString(s, " ")
}

func readMig915(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(mig915File)
	if err != nil {
		t.Fatalf("read %s: %v", mig915File, err)
	}
	return string(b)
}

// mig915Root 定位仓根（包目录 sql/migrations/startup 在仓下三层；
// 与 mig902Tree 同款口径，各自持一份避免跨文件口径漂移）。
func mig915Root(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// TestMigration915BodyMatchesRuntimeSSOT —— 迁移的函数体 ⇔ 运行时 SSOT。
//
// 函数被 provider 候选查询 CROSS JOIN LATERAL 调用 ⇒ 两者漂移的后果是
// 「迁移装一个体、每次 boot 换另一个体」，症状与「上游全挂」同形 ⇒
// 必须在静态层拦住（真库行为由 db 包的 TestRecentCacheHitRateEnsureRealDB
// 与部署门 dd_verify_candidate_fn_deps 分层覆盖）。
func TestMigration915BodyMatchesRuntimeSSOT(t *testing.T) {
	src := readMig915(t)
	runtime := db.RecentCacheHitRateEnsureSQLForTest()
	if strings.TrimSpace(runtime) == "" {
		t.Fatal("db.recentCacheHitRateEnsureSQL is empty — detector is broken")
	}

	// 从运行时 SSOT 抽函数体（AS $$ 之后第一个 $$ 闭合——LastIndex 会把
	// 文件尾巴误当函数体，890 门同坑同修）。
	open := strings.Index(runtime, "AS $$")
	if open < 0 {
		t.Fatalf("cannot locate the function body opener in the runtime SSOT:\n%s", runtime)
	}
	closeIdx := strings.Index(runtime[open+len("AS $$"):], "$$")
	if closeIdx < 0 {
		t.Fatalf("cannot locate the function body closer in the runtime SSOT:\n%s", runtime)
	}
	ssotBody := runtime[open+len("AS $$") : open+len("AS $$")+closeIdx]

	i := strings.Index(src, "CREATE OR REPLACE FUNCTION public.recent_cache_hit_rate")
	if i < 0 {
		t.Fatal("915 must create recent_cache_hit_rate — it is called on the provider " +
			"candidate path (CROSS JOIN LATERAL); its absence is a 100% traffic failure")
	}
	seg := src[i:]
	o2 := strings.Index(seg, "AS $$")
	if o2 < 0 {
		t.Fatalf("cannot locate the function body opener inside 915:\n%s", seg[:min(len(seg), 800)])
	}
	c2 := strings.Index(seg[o2+len("AS $$"):], "$$")
	if c2 < 0 {
		t.Fatalf("cannot locate the function body closer inside 915:\n%s", seg[:min(len(seg), 800)])
	}
	migBody := seg[o2+len("AS $$") : o2+len("AS $$")+c2]

	if normalizeSQL915(migBody) != normalizeSQL915(ssotBody) {
		t.Fatalf("915's recent_cache_hit_rate body and db.recentCacheHitRateEnsureSQL have drifted.\n"+
			"Consequence: the gateway's boot-time CREATE OR REPLACE would swap the body the "+
			"migration installed (or vice versa) ⇒ candidate query behavior changes on the "+
			"next boot ⇒ a silent routing-semantics flip that reads like 'all upstreams down'.\n"+
			"--- migration ---\n%s\n--- runtime SSOT ---\n%s", migBody, ssotBody)
	}
}

// TestMigration915ExecutableTextHasNoRetiredFamilyRefs —— 可执行文本里
// 不得出现 v1 族表/列名（事故第二层根因）。
//
// ⚠ 先剥注释再判：文件头的追记注释按名提及 request_logs_hot 是合法的
// 史实记载（它记录的就是首版怎么坏的）；只有可执行文本里的引用才致命。
func TestMigration915ExecutableTextHasNoRetiredFamilyRefs(t *testing.T) {
	executable := stripComments915(readMig915(t))

	detector := func(s string) []string {
		var hits []string
		for _, unwanted := range []string{
			"FROM public.request_logs_hot", "FROM request_logs_hot",
			"FROM _request_logs_hot", "FROM _request_logs",
			"FROM request_logs", "outbound_model", "client_model",
		} {
			if strings.Contains(s, unwanted) {
				hits = append(hits, unwanted)
			}
		}
		return hits
	}

	if hits := detector(executable); len(hits) > 0 {
		t.Fatalf("915 的可执行文本仍引用 v1 族 %v —— v1 族已随迁移 890 整体 DROP："+
			"族退役库上 CREATE 时校验表引用即 42P01（迁移腿中止），旧列名则 42703。"+
			"2026-10-28 211 事故第二层根因，不许回流。", hits)
	}

	// 阳性对照：同一检测器喂首版坏体样本必须命中（否则上面的负向断言恒绿）。
	const badFirstCut = "SELECT prompt_tokens, cache_read_tokens FROM public.request_logs_hot " +
		"WHERE credential_id = p_credential_id " +
		"AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)"
	if hits := detector(badFirstCut); len(hits) == 0 {
		t.Fatal("阳性对照失效：首版坏体样本没有被检测器命中 —— 负向判据此时测不到任何东西")
	}

	// 会话族正确形态必须在场（防「负向判据过了、正确腿也没了」的空集假绿）。
	for _, want := range []string{
		"FROM public.session_turns_hot",
		"credential_id ~ '^[0-9]+$'",
		"lower(COALESCE(model, canonical_model)) = lower(p_raw_model)",
		"NOT IN ('self_check', 'node_probe', 'system_health')",
	} {
		if !strings.Contains(executable, want) {
			t.Errorf("915 可执行文本缺会话族正确形态片段 %q", want)
		}
	}
}

// TestMigration915DownDropsExactSignature —— down 必须精确删 4 参签名
// （与候选查询的 3 实参 + 1 默认调用形态一致；删错签名 = 假回滚）。
func TestMigration915DownDropsExactSignature(t *testing.T) {
	b, err := os.ReadFile("915_recent_cache_hit_rate.down.sql")
	if err != nil {
		t.Fatalf("read down file: %v", err)
	}
	down := string(b)
	if !strings.Contains(down,
		"DROP FUNCTION IF EXISTS public.recent_cache_hit_rate(bigint, text, integer, integer);") {
		t.Fatalf("down 文件必须 DROP 精确的 4 参签名，当前内容：\n%s", down)
	}
}

// TestMigration915RegisteredInEveryDeliveryChannel —— 三条投递通道登记核对
//（事故第一层根因：首版只登记了 installer 侧，部署序列 files=() 漏了它 ⇒
// 升级库永远轮不到 ⇒ 函数从未建）。
//
//	· installer embeddata：字节级一致（那份是逐字执行的，不是等价执行）；
//	· installed_startup_migrations.tsv：登记行在场；
//	· scripts/apply-db-revision-sequence.sh：files 数组含本文件。
func TestMigration915RegisteredInEveryDeliveryChannel(t *testing.T) {
	root := mig915Root(t)
	// embeddata 副本逐字节相同。
	canonical, err := os.ReadFile(mig915File)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	embeddata, err := os.ReadFile(filepath.Join(root, "installer",
		"cmd", "llm-gw-installer", "embeddata", "startup", mig915File))
	if err != nil {
		t.Fatalf("read embeddata copy: %v (installer 通道未登记 915)", err)
	}
	if string(canonical) != string(embeddata) {
		t.Errorf("canonical 与 installer embeddata 两份必须逐字节相同（embeddata 逐字执行，" +
			"内容漂移 = 新装库装出另一个函数体）")
	}

	// TSV 登记行。
	tsv, err := os.ReadFile(filepath.Join(root, "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("read installed_startup_migrations.tsv: %v", err)
	}
	if !strings.Contains(string(tsv), "915_recent_cache_hit_rate.sql") {
		t.Errorf("installed_startup_migrations.tsv 未登记 915 —— installer 全新安装链不会执行它")
	}

	// 部署序列 files 数组（升级库通道）。
	seq, err := os.ReadFile(filepath.Join(root, "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("read apply-db-revision-sequence.sh: %v", err)
	}
	if !strings.Contains(string(seq),
		`"$ROOT_DIR/sql/migrations/startup/915_recent_cache_hit_rate.sql"`) {
		t.Errorf("apply-db-revision-sequence.sh 的 files 数组未登记 915 —— " +
			"升级库（部署通道）永远轮不到它 ⇒ 函数从未建 ⇒ 候选查询 42883 ⇒ 100%% 流量失败" +
			"（2026-10-28 211 事故第一层根因，5fd8e600c 已补，本门钉住不许回退）")
	}
}
