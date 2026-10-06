package db

// 启动期自愈契约门（2026-10-06，审计 §10.71）
//
// 钉住 `ensureURSMNodeSnapshotMinIdentityPK` 的三件事：
//  1. **列集合必须与 writer 的 ON CONFLICT 同步** —— 这道自愈若漂了，
//     会把生产往错的方向修，而它自己**不会报错**（DDL 成功）。
//  2. **必须在启动序列里被调用** —— 只测函数不测接线，是本项目反复踩的形态。
//  3. **四条安全边界不能被优化掉**：表不存在 / 已分区 / 拿不到锁 / 失败不冒泡。
//
// ★ 第 1 条是本文件存在的核心理由：db.go 与 writer.go 各写一份列集合，
//   是**故意**的（共享常量会让「基线 ↔ writer」那道门恒真），
//   代价就是必须有一道门替它们看着彼此。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const writerPath = "../domains/ursm/v2/persist/writer.go"

func normCols(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func TestIdentityPKLiteralMatchesWriterOnConflict(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash(writerPath))
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	m := regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\(([^)]*)\)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("writer.go 里没解析到 ON CONFLICT —— writer 形态变了，请同步本判据")
	}
	writerCols := normCols(string(m[1]))
	want := normCols(ursmSnapshotIdentityPKCols)
	if strings.Join(writerCols, ",") != strings.Join(want, ",") {
		t.Errorf("db.go 的自愈列集合与 writer 的 ON CONFLICT 不一致：\n"+
			"  writer.go : [%s]\n  db.go 常量 : [%s]\n"+
			"★ 这条自愈若与 writer 不符，**不会报错**——它会成功地把主键改成一个\n"+
			"   writer 用不了的形状，让写入从「已被修好」退回「永久失败」。",
			strings.Join(writerCols, ", "), strings.Join(want, ", "))
	}
}

// TestIdentityPKEnsureIsWiredIntoBoot —— 接线门。
func TestIdentityPKEnsureIsWiredIntoBoot(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "func (d *DB) ensureURSMNodeSnapshotMinIdentityPK(") {
		t.Fatal("db.go 没有定义 ensureURSMNodeSnapshotMinIdentityPK")
	}
	if !strings.Contains(s, "db.ensureURSMNodeSnapshotMinIdentityPK(migCtx)") {
		t.Error("db.go 的启动序列没有调用 ensureURSMNodeSnapshotMinIdentityPK —— " +
			"函数定义了但没接线，等于没有自愈（变异 M43 同族）")
	}
}

// TestIdentityPKEnsureKeepsItsSafetyBoundaries —— 四条边界。
//
// ★ 这几条不是风格偏好，每条都对应一次真实失败形态
//
//	（注释里逐条写明了代价）。
func TestIdentityPKEnsureKeepsItsSafetyBoundaries(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	s := string(src)
	body := extractFuncBody(t, s, "func (d *DB) ensureURSMNodeSnapshotMinIdentityPK(")

	cases := []struct {
		name string
		why  string
	}{
		{"to_regclass('public.",
			"必须用 to_regclass 而非 '...'::regclass —— 裸 cast 在表不存在时会 raise 而不是返回 NULL，" +
				"而部分置备的库上这张表是可能不存在的（与同文件分区 ensure 同款理由）。\n" +
				"★ 这里只匹配 `to_regclass('public.` 前缀：本函数的 SQL 是从 " +
				"`const table` 拼出来的，源文件里不会出现完整表名的字面量。" +
				"首版门去匹配完整字面量因而误报 —— 那是判据的锚太死，不是代码错了。"},
		{"relkind = 'p'",
			"必须先判父表形态：已分区时**不动**。830 的形态自带 4 列主键，" +
				"在分区父表上 DROP/ADD 主键会牵连全部分区（同 partition_825 门的立论）"},
		{"lock_timeout",
			"拿不到锁就放弃。绝不用长 DDL 锁把线上写入堵在队列里 —— " +
				"启动路径上排队等写入是 830 明确避开的失败模式"},
		{"slog.Warn",
			"★ 自愈失败必须**记 WARN 并返回 nil**，不能把错误冒到 db.Open —— " +
				"那会让网关进 no-DB 模式并触发部署自动回滚（750/partition_825 记过这个形态）"},
	}
	for _, c := range cases {
		if !strings.Contains(body, c.name) {
			t.Errorf("自愈函数体里找不到 %q。\n★ %s", c.name, c.why)
		}
	}

	// ★★ 变异 M68 抓出来的洞：**探针存在 ≠ 探针被使用**。
	//
	// 我第一版只断言函数体里有 `relkind = 'p'`（查询还在探）。
	// 于是把守卫从 `if !exists || partitioned {` 改成 `if !exists {` 时，
	// 查询照跑、结果被丢弃、**门全绿** —— 而代码已经在分区父表上
	// DROP/ADD 主键了。
	//
	// 同族：子串断言量的是「写过这句话」，不是「这句话改变了行为」。
	// 这里必须断言**被探到的值真的进了条件判断**。
	if !strings.Contains(body, "|| partitioned") &&
		!strings.Contains(body, "if partitioned") {
		t.Error("自愈函数探到了 partitioned 却没有用它守卫 —— " +
			"查询仍然返回该值，丢弃它等于在**分区父表**上 DROP/ADD 主键，" +
			"会牵连全部分区（变异 M68：只判探针存在时这道门是绿的）")
	}

	// 只在真的不一致时才 DDL：一致时必须零写。
	if !strings.Contains(body, "if actual == strings.Join(want, \",\")") {
		t.Error("自愈函数没有「列集合一致就直接返回」的短路 —— " +
			"每次启动都 DROP/ADD 主键会把大表反复重写（写放大）且无任何收益")
	}
}

func extractFuncBody(t *testing.T, src, header string) string {
	t.Helper()
	i := strings.Index(src, header)
	if i < 0 {
		t.Fatalf("找不到 %s", header)
	}
	rest := src[i:]
	depth := 0
	started := false
	for j, r := range rest {
		switch r {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return rest[:j+1]
			}
		}
	}
	t.Fatalf("%s 的函数体没有正常闭合", header)
	return ""
}
