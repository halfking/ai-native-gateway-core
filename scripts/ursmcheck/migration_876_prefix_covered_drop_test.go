package ursm_test

// 876_drop_prefix_covered_parent_indexes.sql 的性质门（2026-10-18）
//
// 这条迁移删的是**生产上最热写表**上的索引，所以本门钉的不是「文件存在」
// 而是它**凭什么敢删**。三件事必须恒成立：
//
//  1. 删除目标是**封闭清单** —— 每条 DROP INDEX 都必须落在已核验的 3 条里。
//     迁移是最容易被「顺手再加一条」的文件，而 DROP 不可回滚（只能重建）。
//
//  2. 覆盖关系是**现算的**，不是写死的结论。
//     写死的守卫会随 schema 演化悄悄失效：覆盖方被改名或删掉之后，
//     「反正还有那个」的假设不会有人再检查一次。所以门钉住的是
//     那段从 pg_index 重新证明的形状，而不是「它提到了覆盖」这句话。
//
//  3. 守卫**可证伪**。文件里点名保留的 idx_usage_ledger_part_tenant
//     是这条性质的反向对照组：它没有覆盖方，若守卫恒真就会一起被删。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mig876 = "876_drop_prefix_covered_parent_indexes.sql"

// mig876Read 读 canonical 副本（sql/migrations/startup）。
func mig876Read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", "startup", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// mig876Code 去掉整行注释，只留可执行行。
//
// ★ 必须去注释再判定：本迁移的文件头**大量**引用了索引名、覆盖关系、
//   以及「刻意保留 idx_usage_ledger_part_tenant」这句话。
//   不去注释的话，下面三条断言都会被文件头的说明文字满足 ——
//   门就变成了「文档里提到过就算数」，正是本仓反复踩的形态。
func mig876Code(t *testing.T) string {
	t.Helper()
	var out []string
	for _, ln := range strings.Split(mig876Read(t, mig876), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// TestMig876DropTargetsAreClosedList —— 删除目标必须是封闭清单。
func TestMig876DropTargetsAreClosedList(t *testing.T) {
	code := mig876Code(t)
	allowed := map[string]bool{
		"idx_usage_ledger_part_request_id": true,
		"idx_usage_ledger_part_ts":         true,
		"idx_sessions_session_id":          true,
	}
	var found []string
	for _, ln := range strings.Split(code, "\n") {
		u := strings.ToUpper(strings.TrimSpace(ln))
		if !strings.HasPrefix(u, "DROP INDEX") {
			continue
		}
		for name := range allowed {
			if strings.Contains(ln, name) {
				found = append(found, name)
			}
		}
		// 逐条核对有没有清单外的目标。
		for _, tok := range strings.Fields(ln) {
			tok = strings.TrimSuffix(strings.Trim(tok, `"`), ";")
			if !strings.HasSuffix(tok, "idx_usage_ledger_part_request_id") &&
				!strings.HasSuffix(tok, "idx_usage_ledger_part_ts") &&
				!strings.HasSuffix(tok, "idx_sessions_session_id") &&
				strings.Contains(tok, "_idx") || strings.Contains(tok, "_key") {
				t.Errorf("DROP 语句里出现未核验的目标 %q —— "+
					"删除清单必须是封闭的：每加一条都要重新做覆盖证明与读方普查", tok)
			}
		}
	}
	if len(found) != 3 {
		t.Errorf("期望恰好 3 条已核验的 DROP INDEX，实得 %d（%v）", len(found), found)
	}
	for _, want := range []string{
		"DROP INDEX IF EXISTS public.idx_usage_ledger_part_request_id",
		"DROP INDEX IF EXISTS public.idx_usage_ledger_part_ts",
		"DROP INDEX IF EXISTS public.idx_sessions_session_id",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("缺少幂等删除语句 %q", want)
		}
	}
}

// TestMig876ProvesCoverageAtRuntime —— 覆盖证明必须现算，不能写死。
//
// 这条钉的是**形状**（向量重新定基 + 四项前缀比较 + INCLUDE 覆盖 +
// valid/ready + 拒绝路径），而不是「文件里出现了覆盖两个字」。
func TestMig876ProvesCoverageAtRuntime(t *testing.T) {
	code := mig876Code(t)
	need := []struct {
		what string
		frag string
	}{
		{"pg_index 重新读取索引定义", "FROM pg_index ni"},
		{"键列向量重新定基（oidvector 下界为 0，见 §78）",
			"string_to_array(array_to_string(ni.indkey::int2[], ','), ',')::int2[]"},
		{"排序方向比较", "(c.opts)[1:n.indnkeyatts]  = n.opts"},
		{"opclass 比较", "(c.opl)[1:n.indnkeyatts]   = n.opl"},
		{"排序规则比较", "(c.colls)[1:n.indnkeyatts] = n.colls"},
		{"INCLUDE 覆盖判定",
			"(n.keys || COALESCE(n.incs, '{}'::int2[]))\n              <@ (c.keys || COALESCE(c.incs, '{}'::int2[]))"},
		{"覆盖方必须可用", "ci.indisvalid AND ci.indisready"},
		{"窄索引不得有独立语义", "AND NOT ni.indisunique"},
		{"窄索引不得被约束背着", "NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conindid = ni.indexrelid)"},
		{"证不出来必须整条回滚", "RAISE EXCEPTION"},
		{"拒绝信息必须指名对象", "拒绝删除"},
	}
	for _, n := range need {
		if !strings.Contains(code, n.frag) {
			t.Errorf("876 缺少%s（期望片段 %q）—— "+
				"覆盖关系必须在本文件里现算；写死的守卫会随 schema 演化悄悄失效", n.what, n.frag)
		}
	}
}

// TestMig876GuardIsFalsifiable —— 守卫必须有可证伪的对照组。
//
// idx_usage_ledger_part_tenant (tenant_id, ts) 在父表上**没有覆盖方**
// （没有任何以 tenant_id 开头的更宽索引）。它被点名保留，
// 所以它在可执行行里必须完全不出现。
//
// 这条同时是反向对照：如果哪天有人把守卫改成恒真（例如只判 to_regclass 非空），
// 这条窄索引会在生产上被误删，而门是红的。
func TestMig876GuardIsFalsifiable(t *testing.T) {
	code := mig876Code(t)
	if strings.Contains(code, "idx_usage_ledger_part_tenant") {
		t.Error("idx_usage_ledger_part_tenant 出现在可执行行里 —— " +
			"它在父表上没有覆盖方（§78 的扫面已确认），必须保留。" +
			"它是这条门唯一的反向对照组；它进了 DROP 列表就说明守卫被改成了恒真")
	}
	// 对照组要被**点名保留**在文件头，否则下一个人会以为它不存在。
	full := mig876Read(t, mig876)
	if !strings.Contains(full, "idx_usage_ledger_part_tenant") {
		t.Error("文件头应当点名说明 idx_usage_ledger_part_tenant 被刻意保留及其理由")
	}
	if !strings.Contains(full, "没有覆盖方") {
		t.Error("文件头应当写明保留理由（父表上没有任何以 tenant_id 开头的更宽索引）")
	}
}

// TestMig876LockSemantics —— 锁语义必须与「分区父表」这个前提相容。
//
// PG 不支持在分区父表上 DROP INDEX CONCURRENTLY：写了它必然在生产报错，
// 而报错发生在**已经拿到锁之后**的语句里，会把整个事务拖长。
// 反过来，没有 lock_timeout 则会在写流量后面排队 —— 本仓已为 867/870/873
// 反复吃过「排队而不是快速失败」的亏。
func TestMig876LockSemantics(t *testing.T) {
	code := mig876Code(t)
	if !strings.Contains(code, "SET LOCAL lock_timeout = '2s'") {
		t.Error("必须设 lock_timeout —— 抢不到锁要快速失败，而不是排在写流量后面")
	}
	if !strings.Contains(code, "SET LOCAL statement_timeout") {
		t.Error("必须设 statement_timeout")
	}
	if strings.Contains(strings.ToUpper(code), "DROP INDEX CONCURRENTLY") {
		t.Error("PG 不支持在分区父表上 DROP INDEX CONCURRENTLY —— " +
			"它会在生产直接报错，且发生在已经取到锁之后")
	}
}

// TestMig876SixPointWiring —— 六处接线必须齐。
//
// 漏掉任何一处的后果不同但都不小：漏 embeddata ⇒ 全新装机跑不到；
// 漏 StartupFiles ⇒ 安装器不执行；漏 TSV ⇒ 位置记录断档；
// 漏部署通道 ⇒ 被静默跳过（本仓 845-851 八连红同型）；
// 漏台账 ⇒ sqlguard 判红且运维看不见这条迁移。
func TestMig876SixPointWiring(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", "startup", mig876))
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	sum := sha256.Sum256(canonical)
	wantSHA := hex.EncodeToString(sum[:])

	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", mig876)
	eb, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("read embeddata 副本: %v", err)
	}
	esum := sha256.Sum256(eb)
	if hex.EncodeToString(esum[:]) != wantSHA {
		t.Errorf("embeddata 副本与 canonical 不一致（sha %s vs %s）", hex.EncodeToString(esum[:]), wantSHA)
	}

	checks := []struct {
		path string
		why  string
	}{
		{"installer/cmd/llm-gw-installer/main.go", "main.go 缺 //go:embed —— 全新装机编不出二进制"},
		{"installer/internal/dbinit/runner.go", "runner.go 缺 StartupFiles —— 安装器不会执行它"},
		{"scripts/apply-db-revision-sequence.sh", "部署通道 files=() 缺它 —— 会被静默跳过"},
		{"sql/schema/installed_startup_migrations.tsv", "TSV 缺它 —— 位置记录断档"},
		{"docs/db-changelog.md", "台账缺登记 —— sqlguard 判红且运维看不见"},
	}
	base := filepath.Join("..", "..")
	for _, c := range checks {
		b, err := os.ReadFile(filepath.Join(base, c.path))
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		if !strings.Contains(string(b), mig876) {
			t.Errorf("%s 未提及 %s —— %s", c.path, mig876, c.why)
		}
	}
	// main.go 需要 embed 声明与 map 条目两处。
	mg, _ := os.ReadFile(filepath.Join(base, "installer/cmd/llm-gw-installer/main.go"))
	if !strings.Contains(string(mg), "//go:embed embeddata/startup/"+mig876) {
		t.Error("main.go 缺 //go:embed embeddata/startup/" + mig876)
	}
	if !strings.Contains(string(mg), `"startup/`+mig876+`"`) {
		t.Error(`main.go 缺 map 条目 "startup/` + mig876 + `"`)
	}
	// 台账 sha 必须是文件实测值（历史上出现过手抄 sha 导致 TestRegistryShaIsActuallyTheFile 判红）。
	ch, _ := os.ReadFile(filepath.Join(base, "docs/db-changelog.md"))
	if !strings.Contains(string(ch), wantSHA) {
		t.Errorf("台账里的 sha 与文件实测值不一致 —— 实测应为 %s", wantSHA)
	}
}

// TestMig876DownRecreatesOnParent —— down 必须在父表重建且不带 ON ONLY。
//
// 带 ON ONLY 会留下 0 字节空壳、不级联到分区（870 的文件头专门记了这个坑）。
func TestMig876DownRecreatesOnParent(t *testing.T) {
	down := stripSQLLineComments(mig876Read(t, "876_drop_prefix_covered_parent_indexes.down.sql"))
	for _, want := range []string{
		"CREATE INDEX idx_usage_ledger_part_request_id ON public.usage_ledger (request_id)",
		"CREATE INDEX idx_usage_ledger_part_ts ON public.usage_ledger (ts)",
		"CREATE INDEX idx_sessions_session_id ON public.sessions (session_id)",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("down 缺少 %q —— 回滚必须按名重建，否则 down 是空的", want)
		}
	}
	if strings.Contains(strings.ToUpper(down), "ON ONLY") {
		t.Error("down 里出现 ON ONLY —— 那会留下 0 字节空壳且不级联到分区")
	}
	if !strings.Contains(down, "SET LOCAL lock_timeout = '2s'") {
		t.Error("down 必须同样设 lock_timeout —— 重建大分区索引会持锁")
	}
}
