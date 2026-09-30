package dbinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embeddataRoot 定位 installer 的 embeddata 目录。
func embeddataRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..", "cmd", "llm-gw-installer", "embeddata")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("embeddata root %s: %v", root, err)
	}
	return root
}

// untransactable 是 PostgreSQL 明确不能在事务块内执行的语句形态。
// 标记豁免的文件必须真的含有其中之一，否则豁免无正当理由。
var untransactable = []string{
	"DROP INDEX CONCURRENTLY",
	"CREATE INDEX CONCURRENTLY",
	"REINDEX CONCURRENTLY",
	"CREATE DATABASE",
	"VACUUM",
}

// TestNoTransactionMarkerIsJustified 防豁免扩散：
// 任何携带 dbinit:no-transaction 标记的迁移，都必须真的含有
// PostgreSQL 不允许在事务块内执行的语句。
//
// 为什么不测「标记数量」：数量是易过期数字，且无判别力——多一个标记
// 文件若确实含 CONCURRENTLY 就没有问题。真实不变式是「标记必须被内容
// 证明」，这条既可证伪又不会因无关迁移增删而误报。
func TestNoTransactionMarkerIsJustified(t *testing.T) {
	dir := filepath.Join(embeddataRoot(t), "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}

	marked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if !requiresNoTransaction(b) {
			continue
		}
		marked++

		// 标记必须在注释行里，不能是活跃 SQL。
		upper := strings.ToUpper(string(b))
		justified := false
		for _, needle := range untransactable {
			if strings.Contains(upper, needle) {
				justified = true
				break
			}
		}
		if !justified {
			t.Errorf("%s 声明了 %s 豁免，但不含任何不可事务化语句 %v；"+
				"豁免必须窄，去掉标记", e.Name(), noTransactionMarker, untransactable)
		}
	}

	if marked == 0 {
		t.Fatalf("没有任何迁移携带 %s 标记——若 718/719 已修复，"+
			"请连同本守卫一起删除，而不是留着失效的机制", noTransactionMarker)
	}
	t.Logf("携带事务豁免标记的迁移: %d", marked)
}

// TestNoTransactionMarkerOnlyInComment 确认标记只以注释形式出现。
// 若它能被当作 SQL 注入到活跃语句里，psql 会直接报错——但更糟的是
// 一条被注释掉说明「作者以为豁免生效了」，实际仍在事务里跑。
func TestNoTransactionMarkerOnlyInComment(t *testing.T) {
	dir := filepath.Join(embeddataRoot(t), "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if !requiresNoTransaction(b) {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, noTransactionMarker) {
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				t.Errorf("%s:%d 标记出现在非注释行: %q", e.Name(), i+1, line)
			}
		}
	}
}

// TestRequiresNoTransactionMatching 钉住标记解析本身的判别力。
// 一次「全绿」不能证明匹配逻辑有效——必须有一组会失败的输入。
func TestRequiresNoTransactionMatching(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"注释行标准形态", "-- dbinit:no-transaction\nSELECT 1;", true},
		{"注释行带前导空白", "   --  dbinit:no-transaction  \nSELECT 1;", true},
		{"双横线双空格", "--  dbinit:no-transaction\nSELECT 1;", true},
		{"无标记", "-- 普通注释\nSELECT 1;", false},
		{"标记出现在块注释里视为无效", "/* dbinit:no-transaction */\nSELECT 1;", false},
		{"非注释行出现标记视为无效", "SELECT 1; -- dbinit:no-transaction\n", false},
		{"空内容", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := requiresNoTransaction([]byte(c.in)); got != c.want {
				t.Errorf("requiresNoTransaction(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
