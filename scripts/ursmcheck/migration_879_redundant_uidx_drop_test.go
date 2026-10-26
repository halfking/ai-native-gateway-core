package ursm_test

// 879_drop_session_summaries_redundant_session_key_uidx.sql 的性质门（2026-10-21，§143.16）
//
// 这条迁移删的是**生产热写表**上的一条唯一索引，而它同时是
// `ON CONFLICT (session_key)` 的**仲裁器** ⇒ 删错的后果不是「少个索引」，
// 而是**写入直接失败**（触发器 trg_update_session_summary 的 ON CONFLICT 无处仲裁）。
// 所以本门钉的不是「文件存在」，而是它**凭什么敢删**。四件事必须恒成立：
//
//  1. 删除目标是**封闭清单** —— 只允许 DROP 那一条裸唯一索引。
//     迁移是最容易被「顺手再加一条」的文件，而 DROP 不可回滚（只能重建）。
//
//  2. 守卫是**现算的**，不是写死的结论。写死的守卫会随 schema 演化悄悄失效：
//     主键被改名/删掉之后，「反正还有主键」的假设不会有人再检查一次。
//     所以门钉住的是那段**从 pg_index 重新证明**的形状。
//
//  3. 守卫**可证伪**。反向对照组就在文件里：当**没有别的**可仲裁唯一索引时，
//     迁移必须**不动手**（memora 最小 shape 下表没有主键，那条裸 uidx 就是
//     唯一的仲裁器 ⇒ 删了写入直接失败）。
//
//  4. ★ **三段式闭环**：655 建 → 879 删 → runtime bootstrap **不再重建**。
//     只改迁移不改 bootstrap ⇒ 每次开机被建回来；只改 bootstrap 不改迁移 ⇒
//     全新安装路径（655）仍然留着它。⇒ 门钉住**跨文件**的一致性。
//
// ★ 与 876 那道门同款纪律：**去掉整行注释再判定**——
//   本迁移的文件头大量引用索引名、仲裁器、主键等词，
//   不去注释的话下面的断言会被文件头的说明文字满足 ⇒ 门变成「文档里提到过就算数」。

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig879Name = "879_drop_session_summaries_redundant_session_key_uidx.sql"
	mig879Idx  = "session_summaries_session_key_uidx"
)

// mig879Read 读 canonical 副本（sql/migrations/startup）。
func mig879Read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", "startup", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// mig879Code 去掉整行注释，只留可执行行。
func mig879Code(t *testing.T) string {
	t.Helper()
	var out []string
	for _, ln := range strings.Split(mig879Read(t, mig879Name), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// TestMig879DropTargetIsClosedList —— 删除目标必须是封闭清单。
func TestMig879DropTargetIsClosedList(t *testing.T) {
	code := mig879Code(t)
	drops := strings.Count(code, "DROP INDEX")
	if drops != 1 {
		t.Errorf("迁移里 DROP INDEX 出现 %d 次（期望 1 次）：删除目标必须是封闭清单，"+
			"DROP 不可回滚，只能重建", drops)
	}
	if !strings.Contains(code, "DROP INDEX public."+mig879Idx) {
		t.Errorf("迁移没有 DROP INDEX public.%s：唯一允许的删除目标就是它", mig879Idx)
	}
	// 反向：不许顺手动主键 / per_tenant 约束 / 其它索引。
	for _, forbidden := range []string{
		"DROP INDEX public.session_summaries_pkey",
		"DROP INDEX public.session_summaries_session_key_per_tenant",
		"DROP CONSTRAINT",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("迁移里出现了 %q：只允许删 %s，其余对象（主键 / per_tenant 约束）"+
				"属属主拍板项（§143.16 的 T2 / T3），不得在一条迁移里顺手删", forbidden, mig879Idx)
		}
	}
}

// TestMig879GuardIsComputedAndFalsifiable —— 守卫必须现算，且可证伪。
func TestMig879GuardIsComputedAndFalsifiable(t *testing.T) {
	code := mig879Code(t)

	// ① 守卫的形状：从 pg_index 现算「别的可仲裁唯一索引」。
	for _, want := range []string{
		"FROM pg_index",
		"i.indisunique",
		"i.indpred IS NULL",
		"i.indnkeyatts = 1",
		"a.attname = 'session_key'",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("守卫里缺少 %q：判据必须**现算**（从 pg_index 重新证明"+
				"「还有别的仲裁器」），写死的假设会随 schema 演化悄悄失效", want)
		}
	}
	// ② 可证伪：仲裁器候选必须**排除它自己**，否则「有仲裁器」恒真 ⇒ 守卫恒放行。
	if !strings.Contains(code, "i.indexrelid <> to_regclass('public."+mig879Idx+"')") {
		t.Errorf("守卫没有排除 %s 自己：那样「存在别的仲裁器」在 uidx 在场时恒真 ⇒ "+
			"守卫不可证伪（表没有主键时会把唯一的仲裁器删掉 ⇒ ON CONFLICT 写入失败）", mig879Idx)
	}
	// ③ DROP 必须落在守卫之内（IF … THEN 的同一段）。
	dropAt := strings.Index(code, "DROP INDEX")
	guardAt := strings.Index(code, "IF EXISTS (")
	if guardAt < 0 || dropAt < 0 {
		t.Fatalf("迁移里找不到守卫或 DROP（guard=%d drop=%d）", guardAt, dropAt)
	}
	if guardAt > dropAt {
		t.Errorf("守卫出现在 DROP 之后（guard=%d > drop=%d）：守卫无效", guardAt, dropAt)
	}
	// ④ 删不到东西时必须有可观测的信号（RAISE NOTICE），否则「没删」与「删了」无法区分。
	if !strings.Contains(code, "RAISE NOTICE") {
		t.Error("迁移没有 RAISE NOTICE：「没删（无仲裁器）」与「删了」在日志上无法区分")
	}
}

// TestMig879CanonicalAndEmbeddataCopiesAreIdentical —— 两副本必须逐字一致。
//
// 为什么：installer's go:embed 与 DBA 同步通道读的是**两份不同的文件**，
// 只改一份 ⇒ 两条部署路径行为不同（而现场无法从日志分辨走了哪条）。
func TestMig879CanonicalAndEmbeddataCopiesAreIdentical(t *testing.T) {
	canonical := filepath.Join("..", "..", "sql", "migrations", "startup", mig879Name)
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", mig879Name)
	a, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	b, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("read embeddata: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("canonical 与 embeddata 两副本不一致（canonical %d 字节 / embeddata %d 字节）："+
			"两条部署路径会跑不同的内容", len(a), len(b))
	}
}

// TestMig879BootstrapNoLongerRebuildsIt —— ★ 三段式闭环的第三段。
//
// 迁移 655（applied+verified，不能改）仍会建这条索引 ⇒ 本迁移删掉之后，
// **runtime bootstrap 必须不再重建它**，否则每次开机被建回来（白删）。
// ⇒ 门钉住 bootstrap 的创建守卫：它必须被「session_key 上没有别的可仲裁唯一索引」
// 这个判据挡住。
func TestMig879BootstrapNoLongerRebuildsIt(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "db", "session_summaries_schema.go"))
	if err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "CREATE UNIQUE INDEX "+mig879Idx) {
		t.Fatalf("bootstrap 里没有 CREATE UNIQUE INDEX %s：读方变了，请同步更新这条门", mig879Idx)
	}
	// ★ 用正则而不是字面串：gofmt 会把 ` + 常量 + ` 压成 `+常量+`
	//   ⇒ 门若钉字面形态，跑一次 gofmt 就假红，而语义一点没变。
	guardRe := regexp.MustCompile("IF NOT EXISTS \\(\\s*`\\s*\\+\\s*" +
		"sessionSummariesSessionKeyArbiterSQL\\s*\\+\\s*`\\s*\\) THEN")
	if !guardRe.MatchString(src) {
		t.Errorf("bootstrap 的创建守卫不是判据常量形态（期望 IF NOT EXISTS (` + %s + `) THEN）："+
			"没有守卫 = 每次开机都重建这条 ~67MiB 的重复索引 ⇒ 本迁移删了也会被建回来",
			"sessionSummariesSessionKeyArbiterSQL")
	}
}
