package persist

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 818 的实参顺序门。
//
// 为什么需要这道门：迁移 818 新增 24 列后，writer 的 INSERT 有 46 个占位符
// 和 46 个实参。`PREPARE` 只能证明**列名、占位符数量、类型**三者对齐
// （已在 252 生产库事务内对真实表验过），但证不了**实参顺序**。
//
// 而实参顺序错位是本 INSERT 最危险的一种错法：这 24 个新列的类型
// （bigint / integer / real / boolean / text）互相之间在 PG 的
// 隐式转换下几乎全兼容，PG 会照单全收、把值写进错误的列，
// **不报错、不告警、数据静默损坏**。而且症状极晚出现 ——
// 同一个 `updated_at_ms` 列里混进了 `manual_reason` 的文本，
// 要等到有人排查那批数据时才会发现。
//
// 判据：从 writer.go 源码机械抽出
//   ① INSERT 的列清单（有序列名）
//   ② tx.Exec 的实参清单（r.Field 形式）
// 逐位比对：列名去下划线后忽略大小写，应等于实参的字段名同样规范化后的结果。
//
// 之所以规范化后比对而不是直接比字符串：`updated_at_ms` 的 Go 字段是
// `UpdatedAtMS`（不是 `UpdatedAtMs`），`lat_ewma_ms` 是 `LatEWMAMS`。
// 这些缩写大小写不遵循 Go 的默认 snake→Camel，直接比会假红；
// 去掉下划线再忽略大小写则对全部 24 列都成立，且仍足以发现错位
// （错位必然是「不同的字段名」，不是「大小写不同」）。

var (
	// INSERT 的列清单：从 "INSERT INTO ursm_node_snapshot_min" 后的第一个
	// "(" 起，到 ")\nVALUES" 前止。锚点用后者而不是逗号，避免把
	// VALUES 行的内容吃进来。
	reInsertColumns = regexp.MustCompile(
		`(?s)INSERT INTO ursm_node_snapshot_min\s*\((.*?)\)\s*VALUES`)

	// 实参清单：紧跟 SQL 字符串之后的第一个 "(" 到该 Exec 调用结尾。
	// 限定为 `r.` 前缀的字段引用序列，避免把 retention 参数等其它实参吃进来。
	//
	// 2026-10-19（§143.11）：SQL 被提到包级常量 `snapshotInsertSQL` ——
	// 因为**排序列要从同一份 SQL 派生**（两份各写各的常量必然漂移，
	// 而漂移的后果是静默的：排序列少一维 ⇒ 索引密度从 ~90% 掉回 ~70%）。
	// ⇒ 正则必须同时接受「内联反引号字面量」与「常量名」两种形态。
	//
	// ★ 下面那条 TestExecCarriesTheVeryInsertWeCompareAgainst 就是为此补的：
	//   正则放宽之后，「列清单来自 A、实参来自 B」有可能悄悄成立
	//   （A 是那份 INSERT，B 是另一条语句）⇒ 必须显式把两者绑回同一份 SQL。
	reExecArgs = regexp.MustCompile(`(?s)tx\.Exec\(ctx,\s*(?:` + "`" + `.*?` + "`" + `|snapshotInsertSQL)\s*,\s*(.*?)\)`)
)

// insertArgAliases 登记**列名与实参名不对应**的例外。
//
// 我的判据假设「每列都由 r.<列名的大驼峰> 喂入」，这个假设在两处不成立：
//   - `raw_model_name` 的 Go 字段叫 `RawModel`（不带 _name 后缀）
//   - `payload` 的实参是函数内的局部变量 `payloadArg`，不是 Row 字段
//
// 第一版门没设例外表，直接把这两处报成错位。**那不是漂移，是判据假设过强** ——
// 与其把判据放宽成"不比了"，不如把例外**显式列出来**：这样将来若真的新增
// 一个非同义映射，必须有人在这里登记一次，登记本身就是一个决定。
var insertArgAliases = map[string]string{
	"raw_model_name": "r.RawModel",
	"payload":        "payloadArg",
}

// TestExecCarriesTheVeryInsertWeCompareAgainst 把「列清单」与「实参清单」
// 绑回**同一份 SQL**（2026-10-19，§143.11）。
//
// ★ 为什么需要它：§143.11 把 SQL 提到常量 `snapshotInsertSQL` 之后，
//
//	实参提取的正则放宽到「内联字面量 **或** 常量名」。
//	⇒ 放宽带来一个**新的漏洞**：正则可能从**另一条** Exec 调用里取到实参，
//	而列清单取自那份 INSERT ⇒ 两边对不上号，而门照样绿。
//	⇒ 这条断言把两者绑起来：Exec 用的 SQL 必须**就是**含 INSERT 头的那份。
func TestExecCarriesTheVeryInsertWeCompareAgainst(t *testing.T) {
	src, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	s := stripSQLLineComments(string(src))

	// Exec 的 SQL 参数：内联字面量 ⇒ 取其内容；常量名 ⇒ 取该常量的定义。
	execSQL := ""
	if m := regexp.MustCompile(`(?s)tx\.Exec\(ctx,\s*` + "`" + `(.*?)` + "`" + `\s*,`).FindStringSubmatch(s); m != nil {
		execSQL = m[1]
	} else if m := regexp.MustCompile(`(?s)tx\.Exec\(ctx,\s*snapshotInsertSQL\s*,`).FindStringSubmatch(s); m != nil {
		def := regexp.MustCompile(`(?s)const\s+snapshotInsertSQL\s*=\s*` + "`" + `(.*?)` + "`").FindStringSubmatch(s)
		if def == nil {
			t.Fatal("Exec 用的是 snapshotInsertSQL，但找不到它的常量定义")
		}
		execSQL = def[1]
	} else {
		t.Fatal("找不到携带快照 INSERT 的 Exec 调用")
	}

	if !reInsertHead.MatchString(execSQL) {
		t.Fatalf("Exec 用的 SQL 里没有 INSERT INTO ursm_node_snapshot_min\n" +
			"★ 列清单与实参清单必须来自同一份 SQL，否则这道门在比两个不同的东西。")
	}
}

// Test818InsertColumnOrderMatchesExecArgOrder —— 核心门。
func Test818InsertColumnOrderMatchesExecArgOrder(t *testing.T) {
	src, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	s := stripSQLLineComments(string(src))

	colM := reInsertColumns.FindStringSubmatch(s)
	if colM == nil {
		t.Fatal("could not locate the INSERT column list in writer.go")
	}
	argM := reExecArgs.FindStringSubmatch(s)
	if argM == nil {
		t.Fatal("could not locate the tx.Exec argument list in writer.go")
	}

	cols := splitAndClean(colM[1])
	args := splitAndClean(argM[1])

	if len(cols) != len(args) {
		t.Fatalf("column/arg count mismatch: %d columns vs %d args", len(cols), len(args))
	}
	if len(cols) < 46 {
		t.Fatalf("parsed only %d columns — the regex stopped early, the gate is not "+
			"actually covering the statement it claims to check", len(cols))
	}

	for i := range cols {
		arg := strings.TrimSpace(args[i])
		want, aliased := insertArgAliases[cols[i]]
		if aliased {
			if arg != want {
				t.Errorf("position %d: column %q is fed %q, but the registered "+
					"alias says it should be fed %q", i+1, cols[i], arg, want)
			}
			continue
		}
		want = normalizeIdent(cols[i])
		got := normalizeIdent(strings.TrimPrefix(arg, "r."))
		if want != got {
			t.Errorf("position %d: column %q is fed argument %q — 写反位，"+
				"PG 会静默把值写进错误的列（类型互相兼容，不报错）",
				i+1, cols[i], arg)
		}
	}
}

// Test818GuardIsNotVacuous 确认上面对账真的在比每一个位置。
// 没有这条，上面那道门有可能因为正则改动而只比对前几个位置却照样绿。
//
// 2026-10-18：46 → 47（22 原有 + payload + 24 个 818 新增 + 1 个 875 补齐）。
// 这里改的不是「让门变绿的数字」，而是**账面本身**：875 真的加了一列。
func Test818GuardIsNotVacuous(t *testing.T) {
	src, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	colM := reInsertColumns.FindStringSubmatch(stripSQLLineComments(string(src)))
	if colM == nil {
		t.Fatal("could not locate the INSERT column list")
	}
	cols := splitAndClean(colM[1])
	if len(cols) != 47 {
		t.Fatalf("INSERT column count = %d, want 47 "+
			"(22 原有 + payload + 24 个 818 新增 + 1 个 875 补齐 empty_response_rate_5m)",
			len(cols))
	}
	// 必须逐字出现的锚列：payload 之后的第一个、三个类型差异最大的
	// （bigint / boolean / text），以及 875 新增的那一列 —— 确保抽取没有错位到别处。
	anchors := []string{"payload", "updated_at_ms", "disabled", "last_err", "empty_response_rate_5m"}
	joined := strings.Join(cols, ",")
	for _, a := range anchors {
		if !strings.Contains(joined, a) {
			t.Errorf("anchor column %q missing from the parsed list", a)
		}
	}
}

// stripSQLLineComments 去掉整行的 `--` 注释。
//
// 必须在按逗号切分**之前**做，而不是切分之后再过滤 `--` 开头的片段：
// INSERT 的列清单里有一行 SQL 注释
//
//	generation, payload,
//	-- 818：24 个由 payload 提升出来的 typed 列
//	updated_at_ms, ...
//
// 按逗号切开后，注释与 `updated_at_ms` 落在**同一个**片段里
// （`payload,` 之后的 `\n -- 注释 \n updated_at_ms`），
// 切分后按 `--` 前缀过滤会把 `updated_at_ms` 一起丢掉 ——
// 门会误报「45 列 vs 46 实参」，而真实 INSERT 是对的。
// 这不是假警报的推测：门第一次跑就报了 45/46，逐位核对后确认是门自己的缺陷。
func stripSQLLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func splitAndClean(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func normalizeIdent(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", ""))
}
