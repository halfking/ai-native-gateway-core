package sqlguard

// 迁移 SQL 里的 `COMMENT ON INDEX` **三分名**守卫。
//
// ★ 起因（2026-11-24 12:03，211 / acc-pg17 首次投递 983c 当场失败）：
//
//	ERROR: cross-database references are not implemented:
//	       "public.request_metrics.idx_request_metrics_occurred_cursor"
//
// ★ 根因：PostgreSQL 的 `COMMENT ON INDEX <name>` 里，<name> 是
//
//	**schema-qualified 的索引名**（`public.idx_xxx`），
//	**不是**「表名.索引名」。
//	⇒ 写成三分名（schema.table.index）时，PG 把它当作
//	  「跨库引用」（catalog.schema.object）⇒ 直接报错。
//	⇒ 三分名只在 `COMMENT ON TABLE schema.table`、
//	  `COMMENT ON COLUMN schema.table.col` 这类**对象路径**上成立；
//	  索引是**独立命名空间**的对象 ⇒ 没有 `table.index` 这种写法。
//
// ★ 为什么值得立这道门：
//
//	① 这类错误**只在真正执行时**才暴露 ⇒ 编译、单测、迁移顺序门全绿也拦不住
//	   （★ 「门全绿 ≠ 门覆盖我」的又一例）。
//	② 它落在**部署的关键路径**上 ⇒ 一条 COMMENT 让整个部署中止
//	   （本次即如此：DB 修订序列失败 ⇒ 部署中止 ⇒ 靠事务回滚才没留半截状态）。
//	③ 判据是**纯语法形态**（三分名 vs 两分名）⇒ 可以静态检出，
//	   不必真的连库执行。
//
// ★ 门的作用面：**全仓所有迁移 SQL**（canonical / embeddata 副本 /
//
//	installer/migrations 副本）⇒ 新迁移写了三分名 ⇒ 立刻红，
//	不必等到某天部署时才炸。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// commentOnIndexThreePartRe 匹配 `COMMENT ON INDEX schema.table.index`。
//
// ★ 注意只匹配**索引**对象 —— `COMMENT ON TABLE a.b` /
//
//	`COMMENT ON COLUMN a.b.c` 的三分名是**合法**的 ⇒ 不能一刀切禁掉。
var commentOnIndexThreePartRe = regexp.MustCompile(`(?i)COMMENT\s+ON\s+INDEX\s+[A-Za-z_][A-Za-z0-9_$]*\.[A-Za-z_][A-Za-z0-9_$]*\.[A-Za-z_][A-Za-z0-9_$]*`)

// migrationSQLDirs 是需要扫描的迁移 SQL 根目录（相对仓库根）。
var migrationSQLDirs = []string{
	"sql/migrations",
	"installer/migrations",
	"installer/cmd/llm-gw-installer/embeddata",
}

// TestCommentOnIndexMustNotUseThreePartName 扫全仓迁移 SQL，
// 断言没有 `COMMENT ON INDEX` 用了三分名（schema.table.index）。
//
// ★ 这道门是**通用形态门**（不是针对 983c 的一条）⇒
//
//	新迁移写了三分名 ⇒ 立刻红，不必等到部署时才炸。
func TestCommentOnIndexMustNotUseThreePartName(t *testing.T) {
	root := repoRoot(t)

	found := 0
	for _, dir := range migrationSQLDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("迁移目录不存在: %s（门自身配置错，不是迁移的问题）", base)
		}

		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}

			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			body := string(raw)

			// ★ 跳过纯注释行里的示例（以 `--` 开头的行不是语句）。
			for i, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "--") {
					continue
				}
				m := commentOnIndexThreePartRe.FindString(trimmed)
				if m == "" {
					continue
				}
				found++
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d 用了三分名的 COMMENT ON INDEX：%s\n"+
					"     ⇒ PostgreSQL 把它当「跨库引用」⇒ 执行时报\n"+
					"        ERROR: cross-database references are not implemented\n"+
					"     ⇒ 正确写法是**两分名**：COMMENT ON INDEX public.<索引名>",
					rel, i+1, strings.TrimSpace(m))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", base, err)
		}
	}

	t.Logf("已扫描 %d 个迁移目录；三分名 COMMENT ON INDEX 命中 %d 处", len(migrationSQLDirs), found)
}

// TestCommentOnIndexTwoPartFormIsAccepted 反向对照：两分名写法必须被门接受。
//
// ★ 为什么要有这条：门只钉「不该出现的形态」时，最容易的失效是
//
//	**门把正确写法也判红** ⇒ 大家会绕过门 ⇒ 门就废了。
//	⇒ 用一条「正确写法必须通过」的用例钉住门的判别力。
func TestCommentOnIndexTwoPartFormIsAccepted(t *testing.T) {
	good := []string{
		"COMMENT ON INDEX public.idx_request_metrics_occurred_cursor IS '...';",
		"comment on index public.idx_foo is '...';",
		"COMMENT ON INDEX idx_foo IS '...';",
	}
	for _, s := range good {
		if commentOnIndexThreePartRe.MatchString(s) {
			t.Errorf("合法写法被误判为三分名: %s", s)
		}
	}

	bad := []string{
		"COMMENT ON INDEX public.request_metrics.idx_request_metrics_occurred_cursor IS '...';",
		"COMMENT ON INDEX a.b.c IS '...';",
	}
	for _, s := range bad {
		if !commentOnIndexThreePartRe.MatchString(s) {
			t.Errorf("三分名未被检出: %s", s)
		}
	}
}

// TestCommentOnColumnThreePartFormIsStillLegal 反向对照之二：
// `COMMENT ON COLUMN schema.table.column` 的三分名是**合法**的 ⇒ 门不能误伤。
//
// ★ 这条钉的是「门的判别边界」：门只针对 INDEX，不针对 COLUMN/TABLE。
func TestCommentOnColumnThreePartFormIsStillLegal(t *testing.T) {
	legal := []string{
		"COMMENT ON COLUMN public.request_metrics.raw_model_name IS '...';",
		"COMMENT ON TABLE public.request_metrics IS '...';",
	}
	for _, s := range legal {
		if commentOnIndexThreePartRe.MatchString(s) {
			t.Errorf("合法写法被误判: %s", s)
		}
	}
}
