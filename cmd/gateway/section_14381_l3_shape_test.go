//go:build !integration

package main

// §143.81 的门：把「★ 累计量 ≠ 现时成本」与「★ 当前形态的结构指纹」钉成结构判据。
//
// ★★★★ 为什么这一轮要有这道门（★ §143.81 的起因）：
//
//	§143.79 / §143.80 把 pg_stat_statements 的【累计量】当成了【现时成本】：
//	「读链合计 74.4 分钟、L3 占 88.1%」（★ 4.1 天的历史累计）。
//	⇒ ⇒ §143.81 实测发现：★ 那三条 L3 形态里【最贵的两条 delta_calls = 0】
//	⇒ ⇒ ⇒ ★★ 它们随 v1 族退役而【早已跑不动了】⇒ ⇒ 它们的累计耗时
//	⇒ ⇒ ⇒ 【是历史成本，不是现时成本】⇒ ⇒ ⇒ **旧结论的前提被推翻**。
//
// ★★ 这道门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//
//	① ★★★★ 【当前形态】必须仍是 LATERAL + session_bodies_unified 形态，
//	   且【必须不含 v1 族】⇒ ⇒ 这是「哪个 queryid 是当前代码」这条判据的仓内锚点。
//	② ★★★★ 复算脚本必须【两次采样取增量】⇒ ⇒ 不许只读累计值。
//	③ ★★ 复算脚本【不许建任何对象】（★ 生产护栏拒绝行首 DDL）。
//	④ ★★★ PL/pgSQL 块内【不许用 psql 变量注入】（★ 本轮实测：psql **不会**在
//	   dollar-quoted 字符串内替换变量 ⇒ ⇒ 参数化在那条路上走不通）。
//	⑤ ★★★ PL/pgSQL 的 RAISE【只支持裸 %】⇒ ⇒ 不许出现宽度/类型格式说明符
//	   （★ 本轮实测：`%-28s` 被原样输出、`%>` 报 too many parameters）。
//	⑥ ★★★ 差分器必须【并列两个口径】（累计口径 / 现时口径）⇒ ⇒ 缺一个
//	   就会被读成另一个。
//	⑦ ★★ 生产执行器必须取【真实】退出码（★ 不是管道最后一环的）。
//	⑧ ★★★★ 【旧结论必须就地订正】⇒ ⇒ 订正横幅必须真的在 runbook 里。

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14381LegValueScript = "scripts/audit/verify_l3_leg_value.sql"
	section14381DiffScript     = "scripts/audit/diff_l3_snapshots.py"
	section14381ProdRunner     = "scripts/audit/run_prod_sql.sh"
	section14381Runbook        = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14381Lessons        = "LESSONS.md"
	section14381Agents         = "AGENTS.md"

	// ★★★ §143.81 登记面（★ 生产实测确认的「当前形态」queryid）
	//   ⇒ L3_gamma = -7776241007544382269（★ 唯一 ALIVE 的 L3 形态）
	//   ⇒ L3_alpha = 8794675561828039885（★ delta=0 ⇒ 已死）
	//   ⇒ L3_beta  = 3955058327330889760（★ delta=0 ⇒ 已死）
	section14381AliveL3QueryID = "-7776241007544382269"
)

func repoRoot14381(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14381(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14381(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// stripSQLComments14381 去掉 SQL 的行注释与块注释。
//
// ★ 用途：★ 「代码形态」类判据不许被注释里的引用误伤（★ §143.73 纪律）。
func stripSQLComments14381(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	out := b.String()
	for {
		i := strings.Index(out, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "*/")
		if j < 0 {
			out = out[:i]
			break
		}
		out = out[:i] + out[i+j+2:]
	}
	return out
}

// dollarQuotedBlocks14381 取出全部 dollar-quoted 块（★ 本仓只用 $do$）。
//
// ★ 用途：★ 判据④要【只在块内】查 psql 变量注入 ⇒ ⇒ 块外不该被误伤。
func dollarQuotedBlocks14381(s string) []string {
	re := regexp.MustCompile(`(?s)\$do\$(.*?)\$do\$`)
	var blocks []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		blocks = append(blocks, m[1])
	}
	return blocks
}

// TestSection14381CurrentShapeIsLateralUnifiedWithoutV1（★ 判据①）
//
// ★★★★ 【当前形态】是「哪个 queryid 是当前代码」这条判据的仓内锚点：
//
//	★ 当前代码的 L3 冷启动式 = LATERAL + public.session_bodies_unified 形态；
//	★ 它【必须不含 v1 族】⇒ ⇒ 这是迁移 890（v1 族整体退役）的退役守卫；
//	★ ⇒ ⇒ 生产上「当前形态」的 queryid 才有意义（★ §143.81 实测：
//	  当前形态 = L3_gamma = -7776241007544382269 ⇒ ★ 它是三条 L3 里唯一 ALIVE 的）。
//
// ★★ 形态判据【必须查结构】（★ 子句 + 表名），而不是查某个裸子串。
func TestSection14381CurrentShapeIsLateralUnifiedWithoutV1(t *testing.T) {
	shape := lastOutboundSessionSQL
	if strings.TrimSpace(shape) == "" {
		t.Fatal("★ `lastOutboundSessionSQL` 是空的 —— 当前形态的锚点没了")
	}

	// ★ 必须包含的结构标记（★ 逐项写清为什么是它）
	for _, c := range []struct {
		needle string
		why    string
	}{
		{"LATERAL", "★ final_full 快照腿靠 LATERAL 取最新一条 ⇒ 缺它 ⇒ 当前形态就退化成另一形态"},
		{"public.session_bodies_unified", "★ 快照腿的唯一来源 ⇒ 缺它 ⇒ 形态指纹对不上生产"},
		{"public.session_turns_hot", "★ turns 侧的 hot 腿 ⇒ 缺它 ⇒ 不是 SessionFamilyTurnsForSessionSQL"},
		{"public.session_turn_details_hot", "★ details 侧 hot 腿 ⇒ 缺它 ⇒ turns 投影会缺列"},
		{"public.session_bodies_hot", "★ bodies 侧的 hot 腿 ⇒ 缺它 ⇒ bodies 源不完整"},
		{"COALESCE(ff.outbound_body, sb.outbound_body)", "★ 正文取值的唯一口径 ⇒ 变了就换了形态"},
		{"ORDER BY rl.ts DESC", "★ 最新的那一轮 ⇒ 变了就换了形态"},
	} {
		if !strings.Contains(shape, c.needle) {
			t.Errorf("★ 当前 L3 形态里没有 %q —— %s", c.needle, c.why)
		}
	}

	// ★★★ 退役守卫：★ 当前形态【必须不含 v1 族】
	//   ⇒ v1 族已被迁移 890 整体退役（★ 生产 to_regclass 三个名字全 NULL）
	//   ⇒ ⇒ 若它回来了 ⇒ 必然 42P01 ⇒ ⇒ 响亮失败优于静默走错形态
	if strings.Contains(shape, "_request_logs") {
		t.Error("★ 当前 L3 形态里又出现了 `_request_logs` —— " +
			"★ ★★ v1 族已在迁移 890 整体退役（★ 生产 to_regclass 三个名字全 NULL）" +
			"★ ⇒ ⇒ 一旦引用它 ⇒ ⇒ 必然 42P01 ⇒ ⇒ 「会话族没命中」会从「回退」变成「整条查询报错」")
	}

	// ★★ 形态指纹（★ 结构骨架的 md5）⇒ ⇒ 供人工/脚本与生产 Q5/Q6 的 md5 对照
	t.Logf("§143.81 当前形态结构骨架指纹 = %s", section14381ShapeFingerprint(shape))
}

// section14381ShapeFingerprint 算「结构骨架」的 md5（★ 投影列与字面量不参与）。
//
// ★ 用途：★ 「当前形态」要能被唯一指认 ⇒ ⇒ 指纹必须【对投影列不敏感】
//
//	（★ 投影列是 113 列实现细节 ⇒ 变了不该判成换了形态）
//	但【对子句与表名敏感】⇒ ⇒ 表名/子句一变 ⇒ 指纹就变。
func section14381ShapeFingerprint(shape string) string {
	// ★ 去字面量
	lit := regexp.MustCompile(`'[^']*'`)
	s := lit.ReplaceAllString(shape, " ")
	// ★ 去最外层 SELECT 的投影列表（★ 保留 FROM 之后的结构）
	if i := strings.Index(strings.ToUpper(s), " SELECT "); i >= 0 {
		j := strings.Index(strings.ToUpper(s[i:]), " FROM ")
		if j > 0 {
			s = s[:i+len(" SELECT ")] + " ... " + s[i+j:]
		}
	}
	// ★ 只保留结构性 token：关键字、限定名、标点
	toks := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.]*|\(|\)|,|=|\$1|\$2`).FindAllString(s, -1)
	// ★ 去掉占位投影
	kept := make([]string, 0, len(toks))
	for _, tk := range toks {
		if tk == "..." {
			continue
		}
		kept = append(kept, strings.ToUpper(tk))
	}
	sum := md5.Sum([]byte(strings.Join(kept, " ")))
	return hex.EncodeToString(sum[:])
}

// TestSection14381LegValueScriptMustMeasureIncrement（★ 判据②）
//
// ★★★★ 复算脚本的核心义务是【取增量】，不是【读累计】：
//
//	★ pg_stat_statements 的 calls / rows / total_exec_time 只增不减
//	★ ⇒ ⇒ 「累计值大」≠「现在还在变大」
//	★ ⇒ ⇒ 判定「此刻还在不在跑」【必须】看两次采样的差
func TestSection14381LegValueScriptMustMeasureIncrement(t *testing.T) {
	code := stripSQLComments14381(readRepoFile14381(t, section14381LegValueScript))

	// ① 必须有【采样间隔】（★ 没有它就没有增量）
	if !strings.Contains(code, "pg_sleep") {
		t.Error("★ 复算脚本里没有采样间隔（pg_sleep）—— " +
			"★ ⇒ ⇒ 两次采样在同一瞬间 ⇒ ⇒ 差分恒为 0 ⇒ ⇒ 「ALIVE」永远是假")
	}
	// ② 必须有【两次采样】（★ jsonb_object_agg 出现 ≥2 次 ⇒ 两份快照）
	if n := strings.Count(code, "jsonb_object_agg"); n < 2 {
		t.Errorf("★ 复算脚本里只采样了 %d 次 —— "+
			"★ ★★ 取增量至少要两份快照 ⇒ ⇒ 少了第二次 ⇒ ⇒ 差分口径不成立", n)
	}
	// ②★ ★★★ 必须有【两份不同的采样目标】（★ 只数 jsonb_object_agg 不够）
	//   ★ 变异 M2 把第二个采样改成 `INTO t1_DISABLED` ⇒ ⇒ 次数仍是 2 ⇒ ⇒ 门曾【没牙】
	//   ★ ⇒ ⇒ 必须同时认 t0 与 t1 两个落点。
	//   ★★★ 必须查【完整形态】⇒ ⇒ 用词边界（★ RE2 支持 \b）
	//     ★ 变异 M2 把落点改成 `INTO t1_DISABLED` ⇒ ⇒ 「INTO t1」子串仍在 ⇒ ⇒ 门曾【没牙】
	//     ⇒ ⇒ ★★ 裸子串会让「把落点改坏」的变异逃过门（★ §143.73 同族纪律）
	for _, target := range []struct{ name, pattern string }{
		{"INTO t0", `\bINTO t0\b`},
		{"INTO t1", `\bINTO t1\b`},
	} {
		if !regexp.MustCompile(target.pattern).MatchString(code) {
			t.Errorf("★ 复算脚本里没有 %q —— "+
				"★ ★★ 取增量至少要两份【不同】的快照 ⇒ ⇒ 只有一个落点 ⇒ ⇒ 差分口径不成立"+
				"★ ★ （★ 变异 M2 曾把第二个落点改坏而门没抓到）", target.name)
		}
	}
	// ③ 必须显式输出增量口径
	for _, needle := range []string{"delta_calls", "ALIVE="} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ 复算脚本里没有 %q —— "+
				"★ 增量口径必须显式出现在输出里 ⇒ ⇒ 否则「累计」会被当成「现时」", needle)
		}
	}
	// ④ ★ 必须输出机器可解析的【累计快照行】（★ 跨运行差分要用）
	if !strings.Contains(code, "SNAPSHOT") {
		t.Error("★ 复算脚本没有输出 SNAPSHOT 快照行 —— " +
			"★ ★★ 单次运行的窗口太短（★ 低频腿会被采成 delta=0）⇒ ⇒ " +
			"★ 必须能跨两次运行做差 ⇒ ⇒ 才有【任意长窗口】的增量")
	}
	// ⑤★ ★★★ 判别器必须落在【主 WHERE 子句】上（★ 本轮自纠）
	//   ★ 本轮第一版的判别器是「query 里含 outbound_body <> $n::jsonb」
	//   ★ ⇒ ⇒ 实测【同时命中 L3_beta 与 L3_gamma】⇒ ⇒ 判出来的「当前形态」是错的
	//   ★ ⇒ ⇒ 原因：★ L3_beta 的 LATERAL 子查询里【已经】有 jsonb 判据
	//   ★ ⇒ ⇒ 真正的差异在【主 WHERE】：gamma 有两处（sb 与 ff 各一处）
	//   ★ ⇒ ⇒ ★ 判别点挑错 ⇒ ⇒ 「当前形态」判错 ⇒ ⇒ 后面全错
	//
	// ★★★★★★ 2026-10-23（§143.84）★ 本判据的 needle 【就地订正】，★ 原文保留：
	//   ★ §143.84 用 Q8c 逐字量到【当前形态（gamma）的主 WHERE 真实文本】：
	//   ★     AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <> $64::jsonb)
	//   ★          OR (ff.outbound_body IS NOT NULL AND ff.outbound_body <> $65::jsonb))
	//   ★ ⇒ ⇒ ★★ 【是【双左括号】】⇒ ⇒ 本判据原来写的【单左括号】needle
	//   ★ ⇒ ⇒ ★★ 【只匹配 beta/alpha 那种【单分支】旧形态】⇒ ⇒ ★★ 也就是说：
	//   ★ ⇒ ⇒ ★★★ 【这条门原本钉住的，是一个【会指向已死腿】的判别器】
	//   ★ ⇒ ⇒ ★★★ 而 §143.84 修好脚本 needle 后，它与 §143.84 的门【互斥】
	//   ★ ⇒ ⇒ ★★★ （★ 一个要求单括号存在、一个要求单括号出现 0 次 ⇒ ⇒ 必有一个红）
	//   ★ ⇒ ⇒ ⇒⇒ **⇒ 修 needle 必须同步修【守这条 needle 的门】，★ 否则会出现互斥的门。**
	//   ★ ⇒ ⇒ ⇒⇒ 双括号形态【恰好】能区分 gamma（当前形态）与 beta（旧形态）⇒ ⇒ 用它。
	if !strings.Contains(code, "AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <>") {
		t.Error("★ 复算脚本的【当前形态判别器】没有落在【主 WHERE 子句】上 —— " +
			"★ ★★ 本轮实测：用「query 含 outbound_body <> $n::jsonb」当判别器会【同时命中 " +
			"★ L3_beta 与 L3_gamma】（★ 因为 beta 的 LATERAL 子查询里已经有 jsonb 判据）" +
			"★ ⇒ ⇒ 判出来的「当前形态」是【错的】⇒ ⇒ 必须查主 WHERE 的完整形态" +
			"★ ⇒ ⇒ ★★ 且必须用【当前形态的真实形态】（★ 双左括号 `AND ((sb.…`）—— " +
			"★ 单左括号只匹配 beta/alpha 那种【旧形态】⇒ ⇒ 会把【已死的腿】判成「当前形态」")
	}
	// ⑤ ★ 必须有【形态指纹】（★ 结构判据的库侧一半）
	if !strings.Contains(code, "md5(query)") {
		t.Error("★ 复算脚本没有按 md5(query) 做【形态指纹】—— " +
			"★ ★★ 只看 queryid 分不出「当前形态」⇒ ⇒ 必须按【文本指纹】分组 ⇒ ⇒ " +
			"★ 才能把「历史形态」与「当前形态」分开")
	}
}

// TestSection14381LegValueScriptCreatesNoObjects（★ 判据③）
//
// ★★ 生产护栏【拒绝行首的 CREATE / DDL】⇒ ⇒ 复算脚本必须【不建任何对象】。
//
// ★★ 这条判据不是洁癖：★ 一旦脚本里出现 DDL ⇒ ⇒ 它在生产上会被护栏直接拒
// ⇒ ⇒ ⇒ 「跑不出来」会被误读成「跑通了」⇒ ⇒ ⇒ 与 §143.80 那条「rc=0 是假的」同族。
func TestSection14381LegValueScriptCreatesNoObjects(t *testing.T) {
	code := stripSQLComments14381(readRepoFile14381(t, section14381LegValueScript))
	for _, kw := range []string{
		"CREATE TABLE", "CREATE TEMP", "CREATE TEMPORARY", "CREATE OR REPLACE FUNCTION",
		"CREATE INDEX", "CREATE VIEW", "DROP ", "ALTER ", "TRUNCATE", "VACUUM", "REINDEX",
	} {
		if strings.Contains(strings.ToUpper(code), kw) {
			t.Errorf("★ 复算脚本里出现了 %q —— "+
				"★ ★★ 生产护栏拒绝行首 DDL ⇒ ⇒ 脚本在生产上跑不起来 ⇒ ⇒ "+
				"★ 而「跑不起来」很容易被误读成「跑通了」⇒ ⇒ 取样必须用"+
				"★【查询驱动的 FOR 循环 + DO 块 + JSONB 变量】", kw)
		}
	}
}

// TestSection14381NoPsqlVariableInsideDollarQuoted（★ 判据④）
//
// ★★★ 本轮实测踩到的坑：★ **psql 变量不会在 dollar-quoted 字符串（DO 块）内被替换**。
//
//	★ ⇒ ⇒ 「用 psql 变量参数化窗口」这条路在 PL/pgSQL 块里【走不通】
//	★ ⇒ ⇒ 实测报 `syntax error at or near ":"`（★ rc=3）
//	★ ⇒ ⇒ 参数化只能落在【块外】的普通语句上。
//
// ★★ 通用式：★ 「把参数注入到语句里」这条路要先问【注入点在不在替换域内】。
func TestSection14381NoPsqlVariableInsideDollarQuoted(t *testing.T) {
	code := stripSQLComments14381(readRepoFile14381(t, section14381LegValueScript))
	blocks := dollarQuotedBlocks14381(code)
	if len(blocks) == 0 {
		t.Fatal("★ 复算脚本里找不到 dollar-quoted 块 —— 判据④的检查面没了")
	}
	// ★ psql 变量引用形态：`:name` 或 `:'name'` ⇒ ★ 必须排除 PL/pgSQL 自己的 `:=`
	//   ★★★ 注意：★ Go 的 regexp 是 RE2 ⇒ **不支持负向后顾 `(?<!:)`** ⇒ ⇒ 用「匹配 + 手动看前一个字符」
	//     （★ `:=` 天然不匹配，因为 `=` 不在标识符首字符集里）
	varRef := regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`)
	// ★★★ 必须检查【全部】DO 块：★ 第一版写的是 blocks[1:] ⇒ ⇒ 跳过了第一个块
	//   ⇒ ⇒ 而 psql 变量注入恰恰出现在【第一个】块（Q1 的采样间隔）⇒ ⇒ 门【没牙】
	//   ⇒ ⇒ ★ 本条由变异 M5 抓出（★ 变异台是门的元门）。
	for _, b := range blocks {
		for _, loc := range varRef.FindAllStringIndex(b, -1) {
			if loc[0] > 0 && b[loc[0]-1] == ':' {
				continue // ★ `::` 之类的前缀，不是变量引用
			}
			t.Errorf("★ DO 块里出现了 psql 变量引用 %q —— "+
				"★ ★★ psql **不会**在 dollar-quoted 字符串内替换变量 ⇒ ⇒ "+
				"★ 这会报 `syntax error at or near \":\"` ⇒ ⇒ 参数化只能落在【块外】", b[loc[0]:loc[1]])
		}
	}
}

// TestSection14381RaiseFormatMustBeBarePercent（★ 判据⑤）
//
// ★★★ PL/pgSQL 的 RAISE【只支持裸 %】（★ 可选格式说明符只有 %I / %L 这类）。
//
//	★ 本轮实测两个坑：
//	  ① `%-28s` ⇒ 被【原样输出】成 `-28s`（★ 不报错 ⇒ ⇒ 静默错）
//	  ② `%>` ⇒ 报 `too many parameters specified for RAISE`（★ rc=3）
//
// ★★ ① 是静默的 ⇒ ⇒ 特别危险 ⇒ ⇒ 必须有门钉住。
func TestSection14381RaiseFormatMustBeBarePercent(t *testing.T) {
	code := stripSQLComments14381(readRepoFile14381(t, section14381LegValueScript))
	// ★★★ 判据必须只扫【RAISE 的格式串】⇒ ⇒ 不能扫全文
	//   ★ 第一版扫全文 ⇒ ⇒ 脚本里的 `LIKE '%AND (...'` / `'%null'::jsonb` 全被误判
	//   ★ ⇒ ⇒ 「判据要读【被测对象】的那一处」，★ 不是「在文件里搜这个字符」
	raiseRe := regexp.MustCompile(`RAISE\s+(?:NOTICE|WARNING|EXCEPTION)\s+'([^']*)'`)
	bad := regexp.MustCompile(`%[^%\s']`)
	matches := raiseRe.FindAllStringSubmatch(code, -1)
	if len(matches) == 0 {
		t.Fatal("★ 复算脚本里找不到 RAISE 的格式串 —— 判据⑤的检查面没了")
	}
	for _, m := range matches {
		for _, b := range bad.FindAllString(m[1], -1) {
			t.Errorf("★ RAISE 格式串 %q 里出现了 %q —— "+
				"★ ★★ PL/pgSQL 的 RAISE 只支持裸 %% ⇒ ⇒ `%%-28s` 会被原样输出（★ 静默错）、"+
				"★ `%%>` 会报 too many parameters（★ 本轮 rc=3）", m[1], b)
		}
	}
}

// TestSection14381DiffToolMustPublishBothCalibers（★ 判据⑥）
//
// ★★★★ 「累计口径」与「现时口径」必须【并列发布】—— 缺一个就会被读成另一个。
//
//	★ 本轮核心修法：★ §143.79/§143.80 的「74.4 分钟、L3 占 88.1%」是【累计口径】
//	★ ⇒ ⇒ 而属主要做的是【现时口径】的收益重估 ⇒ ⇒ 两个口径必须同时给。
func TestSection14381DiffToolMustPublishBothCalibers(t *testing.T) {
	raw := readRepoFile14381(t, section14381DiffScript)

	for _, needle := range []string{"累计口径", "现时口径"} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 差分器里没有 %q —— "+
				"★ ★★ 两个口径必须并列发布 ⇒ ⇒ 只给累计 ⇒ ⇒ 会被读成现时成本；"+
				"★ 只给现时 ⇒ ⇒ 会丢掉「历史成本已经发生」这个事实", needle)
		}
	}
	// ★ ALIVE 必须由【增量】判定 ⇒ ⇒ 累计为 0 但 delta>0 的腿不能被误判成死的
	if !strings.Contains(raw, "delta_calls") {
		t.Error("★ 差分器没有用 `delta_calls` 判定 ALIVE —— " +
			"★ ⇒ ⇒ 累计值判生死 ⇒ ⇒ 一条「累计很大但早已停跑」的腿会被判成活的")
	}
	if !strings.Contains(raw, "dcalls > 0") {
		t.Error("★ 差分器的 ALIVE 判定不是「delta_calls > 0」—— " +
			"★ ⇒ ⇒ 生死判定必须落在【增量】上 ⇒ ⇒ 累计量只能当历史看")
	}
	// ★ 必须能读多份快照 ⇒ ⇒ 窗口才可任意拉长
	if !strings.Contains(raw, "snaps[0]") || !strings.Contains(raw, "snaps[-1]") {
		t.Error("★ 差分器没有支持「多份快照取首尾」—— " +
			"★ ⇒ ⇒ 单次运行的窗口太短 ⇒ ⇒ 低频腿会被采成 delta=0 ⇒ ⇒ " +
			"★ ⇒ 长窗口复核必须能跨多次运行")
	}
}

// TestSection14381ProdRunnerMustUseRealExitCode（★ 判据⑦）
//
// ★★ 生产执行器必须取【真实】退出码 ⇒ ⇒ 判据要读被测对象的退出码，不是管道最后一环的。
func TestSection14381ProdRunnerMustUseRealExitCode(t *testing.T) {
	raw := readRepoFile14381(t, section14381ProdRunner)

	if !strings.Contains(raw, "pipefail") {
		t.Error("★ 生产执行器没有开 pipefail —— " +
			"★ ⇒ ⇒ 管道的退出码会被最后一环吞掉 ⇒ ⇒ 「rc=0」判据是假的")
	}
	// ★ 必须显式捕获 psql 的退出码并原样传出
	if !strings.Contains(raw, "RC=$?") {
		t.Error("★ 生产执行器没有显式捕获退出码（RC=$?）—— " +
			"★ ⇒ ⇒ 判据读不到被测对象的真实退出码")
	}
	if !strings.Contains(raw, "exit \"${RC}\"") {
		t.Error("★ 生产执行器没有把真实退出码传出（exit ${RC}）—— " +
			"★ ⇒ ⇒ 调用方拿到的还是管道最后一环的退出码")
	}
	// ★ 不许出现「| tail 之后再取 $?」的形态
	if regexp.MustCompile(`\|\s*tail`).MatchString(raw) && regexp.MustCompile(`\$\?`).MatchString(raw) {
		if !strings.Contains(raw, "pipefail") {
			t.Error("★ 生产执行器用了 `| tail` 且没有 pipefail —— " +
				"★ ⇒ ⇒ 真实退出码被 tail 吞掉（★ §143.80 已记录）")
		}
	}
}

// TestSection14381OldReadingsMustBeCorrectedInPlace（★ 判据⑧）
//
// ★★★★ 【错误结论必须就地订正】⇒ ⇒ 订正横幅必须真的在 runbook 里。
//
//	★ §143.79 的「74.4 分钟 / L3 占 88.1%」是【累计口径】⇒ ⇒ 它没写错，
//	★ 但它【被当成了现时成本】⇒ ⇒ ⇒ 这会让属主的收益重估整个跑偏。
//	★ ⇒ ⇒ 订正横幅是本轮【唯一】能防止「旧结论被后来人读成现时」的东西。
func TestSection14381OldReadingsMustBeCorrectedInPlace(t *testing.T) {
	runbook := readRepoFile14381(t, section14381Runbook)

	// ① §143.79 段必须带订正横幅
	idx := strings.Index(runbook, "### §143.79")
	if idx < 0 {
		t.Fatal("★ runbook 里找不到 §143.79 段 —— 旧结论的落点没了")
	}
	seg := runbook[idx:]
	if end := strings.Index(seg, "\n### §143.8"); end > 0 {
		seg = seg[:end]
	}
	for _, needle := range []string{"订正横幅一（2026-10-23 §143.81", "累计", "现时"} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.79 段里没有 %q —— "+
				"★ ★★ 「74.4 分钟 / L3 占 88.1%%」是【累计口径】⇒ ⇒ 它会被读成现时成本"+
				"★ ⇒ ⇒ 属主的收益重估会整个跑偏 ⇒ ⇒ 必须就地订正（★ 原文保留）", needle)
		}
	}

	// ② §143.81 段必须存在，且必须登记「当前形态的 queryid」
	idx81 := strings.Index(runbook, "### §143.81")
	if idx81 < 0 {
		t.Fatal("★ runbook 里找不到 §143.81 段")
	}
	seg81 := runbook[idx81:]
	for _, needle := range []string{section14381AliveL3QueryID, "累计口径", "现时口径"} {
		if !strings.Contains(seg81, needle) {
			t.Errorf("★ §143.81 段里没有 %q —— "+
				"★ 登记面必须留下【可复核的关键词】⇒ ⇒ 否则下一轮无从复核", needle)
		}
	}

	// ③ LESSONS 必须有本轮的条目
	lessons := readRepoFile14381(t, section14381Lessons)
	if !strings.Contains(lessons, "## §271") {
		t.Error("★ LESSONS.md 里没有 §271 —— " +
			"★ ⇒ ⇒ 「累计量 ≠ 现时成本」这条通用式必须落进 LESSONS（★ 跨项目可复用）")
	}
	// ④ AGENTS 必须有本轮的交接段
	agents := readRepoFile14381(t, section14381Agents)
	if !strings.Contains(agents, "R123") {
		t.Error("★ AGENTS.md 里没有 R123 —— " +
			"★ ⇒ ⇒ 本轮的结论与未验项必须落在交接段（★ 否则下一轮 cold-start 会重走一遍）")
	}
}
