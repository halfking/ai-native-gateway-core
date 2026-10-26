//go:build !integration

package main

// §143.92 的门：★ 本轮是【正文族按列拆解 + 形态分类 + 内容级包含关系】轮
// ⇒ ⇒ 产出是【推翻自己上一轮的两个数字与一条结论】+【一把不用 detoast ⇒ 可全量跑的尺子】。
//
// ★★★★★★ 这个门要守的是【§143.92 的中心教训不许退化】：
//
//	① ★★★★★★ 【逐列分解不许退化成单一口径】—— ★ 脚本必须同时给出
//	    ★ `request_delta` / `response_delta` / `outbound_body` 三列的体积
//	    ★ ⇒ ⇒ 这是本轮的核心教训：「按【列】测的量」被当成了「按【对象】测的量」
//	    ★ ⇒ ⇒ 同一个会话 508 KiB ↔ 466 MB ⇒ ⇒ 结论的方向整个翻转
//	② ★★★★★★ 【新的尺子必须还在】—— ★ 「重复字节占比」（★ 只用 `pg_column_size`
//	    ★ ⇒ ⇒ 不 detoast ⇒ ⇒ 可全量跑）★ 必须在脚本里存在
//	    ★ ⇒ ⇒ 否则族级重复量又只能抽样 ⇒ ⇒ 结论就退化成「5 个会话的观察」
//	③ ★★★★★★ 【脚本必须只读】—— ★ 可执行面不许出现写 / DDL 关键字
//	④ ★★★★★★ 【runbook §143.92 段必须登记关键数字】★ 且【两个订正横幅必须在位】
//	⑤ ★★★★ 【LESSONS §286 + AGENTS R136 必须在位】
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 工具形态门】，★ **不是**端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释与 `\echo` 元命令）⇒ ⇒
// ★ ★★ 否则「在注释里写出那行」就会假绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14392Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14392Lessons = "LESSONS.md"
	section14392Agents  = "AGENTS.md"
	section14392Script  = "scripts/audit/verify_session_bodies_column_decomposition.sql"
)

func repoRoot14392(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14392(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14392(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// sqlNonComment14392 返回去掉【整行 `--` 注释】后的行（★ 保留 `\echo` 元命令）。
//
// ★ ★★ 为什么要它：★ 判据① 数的是【段数】⇒ ⇒ 而「被注释掉的 `\echo`」不该算一段
// ★ ★★ ⇒ ⇒ ⇒ 这是本门自己的洞 ⇒ ⇒ 修法是数【非注释行里的 `\echo`】。
func sqlNonComment14392(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14392(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// sqlExecutable14392 返回 SQL 文件的【可执行面】。
//
// ★ ★★ 为什么要剥：★ 本轮的判据（★ 尤其是①③）查的是【真正会被 PG 执行的那部分】⇒ ⇒
// ★ ★★ 若不剥注释，★ 「在 `--` 注释里写出 count(*) / DELETE」就会假绿。
func sqlExecutable14392(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range sqlNonComment14392(t, rel) {
		if strings.HasPrefix(line, `\`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14392ScriptExistsAndHasTenStages（★ 判据①的前置）
func TestSection14392ScriptExistsAndHasTenStages(t *testing.T) {
	n := 0
	for _, line := range sqlNonComment14392(t, section14392Script) {
		if strings.HasPrefix(line, `\echo `) {
			n++
		}
	}
	if n != 10 {
		t.Errorf("★ %s 里有 %d 个（非注释的）`\\echo` 段（期望 10）—— "+
			"★ ★★ 本轮的可复现复算是【10 段】：① 按 kind×列体积分解 ② request_delta 直方图 "+
			"③ outbound_body 直方图 ④ 最大 15 行 ⑤ top20 会话+形态分类 ⑥ 口径③形态分桶 "+
			"⑦ 内容级包含关系 ⑧ 口径②重复字节占比 prev_share ⑨ outbound_body 写入时点+E1 门 "+
			"⑩ 消息条数增长曲线"+
			"★ ⇒ ⇒ ★★ 少一段 ⇒ ⇒ 那个坑就没有被钉住"+
			"★ ⇒ ⇒ ★★ 且【被注释掉的 `\\echo` 不算一段】",
			section14392Script, n)
	}
}

// TestSection14392ColumnDecompositionIsNotSingleMeasure（★ 判据①）—— ★★ 本轮核心教训的守卫
//
// ★★★★★★ 【报「某个对象的体积」必须逐列给 —— ★ 不许只挑一列代表】—— ★★★
// ★ §143.92 实测：★ `gw_omp-global-kxpms` 的 `outbound_body` 一列 ＝ 519,943 B（508 KiB）
// ★ ⇒ ⇒ 而 `request_delta` 一列 ＝ 480,981,980 B ⇒ ⇒ 三列合计 488,976,641 B（466 MB）
// ★ ⇒ ⇒ ⇒ 【全表第 1 大会话】⇒ ⇒ 「按【列】测的量」被当成了「按【对象】测的量」⇒ ⇒ 方向翻转。
func TestSection14392ColumnDecompositionIsNotSingleMeasure(t *testing.T) {
	exe := sqlExecutable14392(t, section14392Script)
	for _, needle := range []string{
		"pg_column_size(request_delta)",  // ★ 真正的主体列
		"pg_column_size(response_delta)", // ★ 中间列
		"pg_column_size(outbound_body)",  // ★ §143.91 只测了这一列 ⇒ ⇒ 结论翻转
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 本轮实测：`gw_omp-global-kxpms` 的 `outbound_body` 只有 244,438 B，"+
				"★ 而 `request_delta` 是 480,981,980 B（★ 差 1,966 倍）"+
				"★ ⇒ ⇒ ★★ 只测一列 ⇒ ⇒ 「按【列】测的量」被当成了「按【对象】测的量」"+
				"★ ⇒ ⇒ ★★ 结论的方向整个翻转（508 KiB ↔ 466 MB）",
				section14392Script, needle)
		}
	}
	// ★★ 三列必须【同一条 select】里并列 —— 分成三条语句 ⇒ ⇒ 又退化成了「挑一列」。
	if !strings.Contains(exe, "sum(pg_column_size(request_delta))  AS req_delta_bytes") ||
		!strings.Contains(exe, "sum(pg_column_size(response_delta)) AS resp_delta_bytes") ||
		!strings.Contains(exe, "sum(pg_column_size(outbound_body))  AS outbound_bytes") {
		t.Errorf("★ %s 的【可执行面】里没有【逐字的三列并列别名】—— "+
			"★ ★★★★ 「按 kind × 列 的体积分解」这一段必须【一条 select 里三列并列】"+
			"★ ⇒ ⇒ ★★ 拆成三条语句 ⇒ ⇒ 读者又会只挑一列引用"+
			"★ ⇒ ⇒ ★★ 这是 §143.91 那个 508 KiB 结论的【同款洞】",
			section14392Script)
	}
}

// TestSection14392PrevShareRulerIsPresent（★ 判据②）—— ★★ 新尺子的守卫
//
// ★★★★★★ 【「重复字节占比」这把尺子必须还在】—— ★★★
// ★ 它只用 `pg_column_size` ⇒ ⇒ **不 detoast** ⇒ ⇒ **可全量跑**（★ 这是它存在的全部理由）
// ★ ⇒ ⇒ 实测族级：★ ≥10 轮 229 会话持有 56.1% 体积、★ 重复字节占比 98.5%
// ★ ⇒ ⇒ ★★ 而内容级口径（★ `@>`）需要 detoast ⇒ ⇒ 只能扫指定会话 ⇒ ⇒ 拿不到族级。
func TestSection14392PrevShareRulerIsPresent(t *testing.T) {
	exe := sqlExecutable14392(t, section14392Script)
	// ★ ★★ `lag(...) OVER (PARTITION BY session_id ORDER BY turn_no)` 是「上一轮」的口径。
	if !strings.Contains(exe, "lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no)") {
		t.Errorf("★ %s 的【可执行面】里没有逐字的 `lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no)` —— "+
			"★ ★★★★ 「重复字节占比」这把尺子靠【上一轮】来算（★ prev_share）"+
			"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「≥10 轮会话 98.5%% 重复」这个族级读数就没有可复现的出处"+
			"★ ⇒ ⇒ ★★ 而内容级 `@>` 需要 detoast ⇒ ⇒ 只能扫 5 个会话 ⇒ ⇒ 族级就丢了",
			section14392Script)
	}
	// ★ ★★ 「等量」口径：两轮的字节数必须【逐字相邻】（★ 当前轮 + 上一轮）。
	for _, needle := range []string{
		"pg_column_size(request_delta) AS cur_bytes",
		"pg_column_size(prev)          AS prev_bytes",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 「重复字节占比」＝ Σ|上一轮字节| / Σ|本轮字节|"+
				"★ ⇒ ⇒ ★★ 两个分子分母必须【逐字相邻给出】⇒ ⇒ 否则「98.5%%」这个数就没法复算",
				section14392Script, needle)
		}
	}
	// ★ ★★ 族级读数必须真的【分桶】出来（★ 单轮 / 2-9 轮 / ≥10 轮）。
	if !strings.Contains(exe, "'1_ge10_turns'") || !strings.Contains(exe, "'3_single_turn'") {
		t.Errorf("★ %s 的【可执行面】里没有【按轮次数分桶】的常量 —— "+
			"★ ★★★★ 本轮实测的族级读数是【≥10 轮 229 会话 ＝ 56.1%% 体积、98.5%% 重复】"+
			"★ ⇒ ⇒ ★★ 而「单轮 38.4%% / 2-9 轮 5.5%% / ≥10 轮 56.1%%」这个三分法"+
			"★ ⇒ ⇒ ★★ 就是「真正的分界线是【轮次数】」这条结论的证据",
			section14392Script)
	}
}

// TestSection14392ContentLevelRulerIsPresent（★ 判据②的配套）—— ★★ 两把尺子必须并排
//
// ★★★★★★ 【「等量」与「逐条相同」是两把不同的尺子 —— ★ 必须都在】—— ★★★
// ★ §143.92 实测：`gw_omp-global-kxpms` 的 `@>` 成立率只有 0.3%，★ 但每轮新增消息数 = 0.0
// ★ ⇒ ⇒ 它不是「新增」，★ 是【每轮重发等量上下文】⇒ ⇒ `@>` 对它失明。
func TestSection14392ContentLevelRulerIsPresent(t *testing.T) {
	exe := sqlExecutable14392(t, section14392Script)
	for _, needle := range []string{
		"request_delta @> prev",    // ★ 逐条包含
		"jsonb_array_length(prev)", // ★ 条数对比
		// ★ ★★★ 这里必须【逐字钉住真正注册它的那一行】—— ★ 不能用裸子串「avg_new_msgs」：
		// ★ ★★ ① 跨行 needle 打不中行内替换（★ 变异会改别名、而门查的字符串不动）；
		// ★ ★★ ② 裸子串会被「改名成后缀版」逃过（★ `avg_new_msgs_x` 仍含 `avg_new_msgs`）。
		// ★ ⇒ ⇒ 所以判据改成【别名 + 它前面那个具体的 round 表达式】的组合形态。
		"jsonb_array_length(prev))", // ★ 条数对比表达式的一部分
		// ★ ★★ 「AS avg_new_msgs」必须带【前面的逗号+空格+右括号】⇒ ⇒ 否则改名成
		// ★ 「avg_new_msgs_x」时裸子串照样命中 ⇒ ⇒ 门没有牙。
		// ★ ★★★ 尾部必须带【换行】⇒ ⇒ 否则改名成「avg_new_msgs_x」时
		// ★ ★★★ 这个 needle 仍会命中 ⇒ ⇒ 门没有牙（★ 变异 M7 的实测）。
		"), 1) AS avg_new_msgs\n",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 实测 `gw_omp-global-kxpms` 的 `@>` 成立率只有 0.3%%，★ 但每轮新增条数 = 0.0"+
				"★ ⇒ ⇒ ★★ 它是【每轮重发等量上下文】⇒ ⇒ `@>` 这把【逐条相同】的尺子对它失明"+
				"★ ⇒ ⇒ ★★ 两把尺子必须【并排】才不误判（★ 少了 `avg_new_msgs` ⇒ ⇒ 「0.3%%」会被读成「不重复」）",
				section14392Script, needle)
		}
	}
}

// TestSection14392ScriptIsReadOnly（★ 判据③）
//
// ★★★★★★ 【脚本必须只读】—— ★ 不许出现写 / DDL 关键字。
var forbidden14392 = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|vacuum|analyze|reindex|cluster|refresh|merge|grant|revoke|copy|create|call|do)\b`)

func TestSection14392ScriptIsReadOnly(t *testing.T) {
	exe := sqlExecutable14392(t, section14392Script)
	for i, line := range strings.Split(exe, "\n") {
		if forbidden14392.MatchString(line) {
			t.Errorf("★ %s 的可执行面第 %d 行出现了写 / DDL 关键字：%q —— "+
				"★ ★★★★ 本脚本是【只读复算】⇒ ⇒ 生产上重复跑它必须绝对安全"+
				"★ ⇒ ⇒ ★★ 只允许 SELECT 与 `SET statement_timeout`",
				section14392Script, i+1, line)
		}
	}
	for _, line := range strings.Split(exe, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "SET ") &&
			!strings.HasPrefix(strings.ToUpper(line), "SET STATEMENT_TIMEOUT") {
			t.Errorf("★ %s 的可执行面出现了非 `SET statement_timeout` 的 SET：%q —— "+
				"★ ★★ 只读脚本的 SET 面要收敛", section14392Script, line)
		}
	}
}

// TestSection14392RunbookRegistersNumbers（★ 判据④）
func TestSection14392RunbookRegistersNumbers(t *testing.T) {
	runbook := readRepoFile14392(t, section14392Runbook)
	i := strings.Index(runbook, "### §143.92")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.92 段 —— ★ 本轮的复算必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"480,981,980",   // ★ gw_omp-global-kxpms 的 request_delta
		"488,976,641",   // ★ 三列合计
		"3,175,827,996", // ★ turn_delta 的 request_delta
		"1,649,078,294", // ★ ≥10 轮会话的 request_delta 体积
		// ★ ★★★ 「重复字节」这个数在 §143.92 段里【合法出现 4 次】（★ 族级表 /
		// ★ ★★★ 体积三分表 / 口径并列表 / 诚实边界）⇒ ⇒ 裸子串判据【改一处仍会命中其余三处】
		// ★ ⇒ ⇒ ⇒ 门没有牙 ⇒ ⇒ 必须【逐字钉住族级表那一行的完整形态】。
		"| **≥10 轮** | **229** | 27,397 | **1,649,078,294** | 1,623,849,268 | **98.5%** | 0.958 |",
		"98.5%",       // ★ 族级 prev_share
		"229",         // ★ ≥10 轮会话数
		"56.1%",       // ★ 它们占该列的比重
		"749,136,709", // ★ 形态 A 复算值（★ 与 §143.73 的 747 MB 对拍）
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.92 段里没有 %q —— "+
				"★ ★★ 本轮的读数是【数字级】的 ⇒ ⇒ 新数字必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 旧的（★ §143.91 的 508 KiB / 1.91 GB）结论就会继续被引用",
				needle)
		}
	}
}

// TestSection14392CorrectionBannersAreRegistered（★ 判据④的配套）
//
// ★ ★★ 订正必须【就地留在被订正的段上】⇒ ⇒ 否则读旧段的人看不到它已被改。
func TestSection14392CorrectionBannersAreRegistered(t *testing.T) {
	runbook := readRepoFile14392(t, section14392Runbook)

	// ★ 横幅八在 §143.91 段之内。
	i := strings.Index(runbook, "### §143.91")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.91 段")
	}
	b := strings.Index(runbook[i+1:], "\n### ")
	if b < 0 {
		b = len(runbook) - i
	}
	part91 := runbook[i : i+b]
	if !strings.Contains(part91, "订正横幅八（2026-10-23 §143.92 轮）") {
		t.Errorf("★ §143.91 段里没有「订正横幅八（2026-10-23 §143.92 轮）」 —— " +
			"★ ★★ §143.91.1 的「长驻会话不是存储问题」与 §143.91.5 的「turn_delta = 1.91 GB」" +
			"★ ⇒ ⇒ ★★ 都已被 §143.92 逐列复算推翻 ⇒ ⇒ 订正必须【就地挂在被订正的段上】（★ 原文保留）")
	}
	// ★ 横幅必须自带【订正后的数】。
	for _, needle := range []string{"488,976,641", "3,175,827,996"} {
		if !strings.Contains(part91, needle) {
			t.Errorf("★ §143.91 段的订正横幅八里没有 %q —— "+
				"★ ★★ 横幅必须自带【订正后的数】⇒ ⇒ 否则读者看不到结论改成了什么",
				needle)
		}
	}

	// ★ 横幅九在 §143.90 段之内（★ 订正的是那里的「订正横幅七」第①条）。
	j := strings.Index(runbook, "### §143.90")
	if j < 0 {
		t.Fatal("★ runbook 里找不到 §143.90 段")
	}
	c := strings.Index(runbook[j+1:], "\n### ")
	if c < 0 {
		c = len(runbook) - j
	}
	part90 := runbook[j : j+c]
	if !strings.Contains(part90, "订正横幅九（2026-10-23 §143.92 轮）") {
		t.Errorf("★ §143.90 段里没有「订正横幅九（2026-10-23 §143.92 轮）」 —— " +
			"★ ★★ §143.90 的「订正横幅七」第①条（★ 作废理由 ＝ 只有 508 KiB）已被 §143.92 部分推翻" +
			"★ ⇒ ⇒ ★★ 而那条横幅【继续留在原地】⇒ ⇒ ⇒ 它必须就地被指正")
	}
	const wantBanner9Reason = "15 次（0.3%）"
	if !strings.Contains(part90, wantBanner9Reason) {
		t.Errorf("★ §143.90 段的订正横幅九里没有 %q —— "+
			"★ ★★ 第①条【换掉的理由】必须给出来（★ 它不是二次方放大）⇒ ⇒ 否则读者只看到「作废」看不到「为什么」",
			wantBanner9Reason)
	}
}

// TestSection14392LessonsAndAgentsAreRegistered（★ 判据⑤）
func TestSection14392LessonsAndAgentsAreRegistered(t *testing.T) {
	if !strings.Contains(readRepoFile14392(t, section14392Lessons), "## §286") {
		t.Error("★ LESSONS.md 里没有 §286 —— " +
			"★ ⇒ ⇒ 「报体积必须同时报测的是哪一列」与「形状判据只对它设计要检测的现象敏感」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14392(t, section14392Agents), "R136 轮") {
		t.Error("★ AGENTS.md 里没有 R136 轮 —— " +
			"★ ⇒ ⇒ 本轮「推翻自己上一轮的两个数字与一条结论」必须落在交接段")
	}
}
