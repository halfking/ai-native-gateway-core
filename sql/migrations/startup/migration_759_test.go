package startup

// migration_759_test.go —— 759「report_snapshots 最细粒度维度」的三处一致性门。
//
// 为什么需要这道门（沿用 745 的 C5 先例）：759 的产物分散在三处，任一处漏改
// 都不会让任何现有测试变红——
//  1. sql/migrations/startup/759_*.sql            权威迁移
//  2. sql/objects/tables/report_snapshots.sql     SSOT 表定义（建库真相）
//  3. db/db.go ensureReportSnapshots              存量库 boot ensure
// 只改 1 不改 2，则新建库与升级库长出不同形状；只改 1 不改 3，则存量库启动后
// 仍缺列，查询要到运行期才炸。
//
// 与 installer parity map 是同族问题：门全绿但被测物没进门。所以下面每条都是
// 「删掉某项必须变红」的写法，而不是只验存在。
//
// 断言一律用**结构化**模式，不能用 strings.Contains：这三份文件里 person /
// grain 等词大量出现在**注释**中（"internal_person"、"person 冗余进 person
// 列"……），子串匹配在真的把列删掉之后依然为真——首版就是这么写的，删列变异
// 验不出红。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	m759Canonical = "759_report_snapshots_grain_dims.sql"
	m759SSOT      = "../../../sql/objects/tables/report_snapshots.sql"
	m759BootDB    = "../../../db/db.go"
)

var (
	// ssotColRe 行首的列定义：只认「这一行真的定义了一个列」。
	ssotColRe = regexp.MustCompile(`(?m)^\s*%s\s+[A-Za-z]`)
	// addColRe 增量加列语句（迁移与 boot ensure 的存量库分支）。
	addColRe = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+%s\b`)
	// idxRe 索引定义，允许 ON 子句换行。
	idxRe = regexp.MustCompile(`(?is)CREATE\s+(UNIQUE\s+)?INDEX\s+(IF\s+NOT\s+EXISTS\s+)?[^;]*?\b%s\b`)
)

// hasAny 用带 %s 占位的模式在 src 里找 name（占位按字面量转义）。
//
// 注意：必须**重新编译**后再拿 src 去匹配。首版写成
// re.MatchString(替换后的模式串)——把模式当主体、拿它匹配自己，恒为 false，
// 于是所有列断言一律报红。是「基线跑一次」而不是「删列变异」先暴露了它。
func hasAny(re *regexp.Regexp, src, name string) bool {
	compiled, err := regexp.Compile(strings.Replace(re.String(), "%s", regexp.QuoteMeta(name), 1))
	if err != nil {
		return false
	}
	return compiled.MatchString(src)
}

func TestMigration759_ThreeWayConsistency(t *testing.T) {
	up, err := os.ReadFile(filepath.Join(m759Canonical))
	if err != nil {
		t.Fatalf("read canonical 759: %v", err)
	}
	upStr, ssotStr, bootStr := string(up), readOrFatal(t, m759SSOT), readOrFatal(t, m759BootDB)

	// 三列：grain 聚合的凭据 / apikey / 用户三个维度。
	cols := []string{"credential_id", "api_key_id", "person"}
	// 两个最细粒度 scope。
	scopes := []string{"daily_grain", "internal_grain"}
	// 四个 partial 索引（idx_..._scope_date 是 745 的既有项，不在此列）。
	idxs := []string{
		"idx_report_snapshots_grain_date",
		"idx_report_snapshots_internal_grain_date",
		"idx_report_snapshots_credential_date",
		"idx_report_snapshots_api_key_date",
	}

	for _, c := range cols {
		if !hasAny(addColRe, upStr, c) {
			t.Errorf("759 迁移缺 ADD COLUMN %q", c)
		}
		if !hasAny(ssotColRe, ssotStr, c) {
			t.Errorf("SSOT report_snapshots.sql 缺列定义 %q（759 已加，建库真相没跟上）", c)
		}
		// boot ensure 有两条路径：新建库走 CREATE TABLE 块，存量库走
		// ADD COLUMN IF NOT EXISTS。两条都要有，否则只覆盖一半场景。
		if !hasAny(ssotColRe, bootStr, c) {
			t.Errorf("db/db.go ensureReportSnapshots 的建表块缺列 %q", c)
		}
		if !hasAny(addColRe, bootStr, c) {
			t.Errorf("db/db.go ensureReportSnapshots 的存量库 ALTER 缺列 %q", c)
		}
	}

	for _, s := range scopes {
		if !strings.Contains(upStr, s) {
			t.Errorf("759 迁移缺 scope %q", s)
		}
		if !strings.Contains(ssotStr, s) {
			t.Errorf("SSOT report_snapshots.sql 缺 scope %q", s)
		}
	}

	for _, i := range idxs {
		if !hasAny(idxRe, upStr, i) {
			t.Errorf("759 迁移缺索引 %q", i)
		}
		if !hasAny(idxRe, ssotStr, i) {
			t.Errorf("SSOT report_snapshots.sql 缺索引 %q", i)
		}
		if !hasAny(idxRe, bootStr, i) {
			t.Errorf("db/db.go ensureReportSnapshots 缺索引 %q（存量库启动后不会建）", i)
		}
	}

	// installer 第五点：embeddata 副本必须与权威迁移逐字节相同。
	// 漏改副本时 installer 会在生产上应用一份过期迁移，而任何静态检查都不红。
	embed, err := os.ReadFile(filepath.Join("..", "..", "..",
		"installer", "cmd", "llm-gw-installer", "embeddata", "startup", m759Canonical))
	if err != nil {
		t.Fatalf("read installer embeddata copy: %v", err)
	}
	if !bytes.Equal(embed, up) {
		t.Error("installer embeddata/startup/759 与权威迁移不一致 —— 五点同步断裂")
	}
}

// TestMigration759_ChangelogRow 钉住 docs/db-changelog.md 的 759 行。
//
// 为什么单独立一道：上面那道门覆盖「权威迁移 ↔ SSOT ↔ db/db.go ↔ installer
// 副本」，但 changelog 是**人读的迁移账本**，它不参与建库、不参与启动，于是
// 漏写或写错没有任何现有门会红——真出现过「迁移已应用、changelog 最后一行还
// 是 758」的状态，两边互相矛盾而全绿。
//
// 断言两件事：
//   - 存在 `| 759 | ` 行（漏写即红）；
//   - 行内记录的 sha256 等于权威迁移文件的实际 sha256（写错即红）。
//     只断言「有这一行」不够——行在但 sha 指向旧内容，等于账本记的是另一份迁移。
func TestMigration759_ChangelogRow(t *testing.T) {
	const changelog = "../../../docs/db-changelog.md"
	doc := readOrFatal(t, changelog)

	rowRe := regexp.MustCompile(`(?m)^\|\s*759\s*\|.*$`)
	row := rowRe.FindString(doc)
	if row == "" {
		t.Fatalf("%s 没有 | 759 | 行：迁移已落地但账本缺行，SSOT 一致性无从核对", changelog)
	}

	if !strings.Contains(row, m759Canonical) {
		t.Errorf("changelog 的 759 行没点名迁移文件 %q：%s", m759Canonical, row)
	}

	// 账本记的 sha 必须等于文件实际内容。
	raw, err := os.ReadFile(m759Canonical)
	if err != nil {
		t.Fatalf("read canonical 759 for sha: %v", err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])
	if !strings.Contains(row, want) {
		t.Errorf("changelog 的 759 行 sha 与文件实际 sha 不一致：\n  文件 = %s\n  账本 = %s",
			want, strings.TrimSpace(row))
	}
}

func readOrFatal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
