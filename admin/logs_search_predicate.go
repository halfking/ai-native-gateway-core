package admin

// logs_search_predicate.go —— `/api/logs?q=` 的**谓词形态决策**。
//
// # 背景（2026-10-20 会话族审计 G-5）
//
// S4 停写后 `/api/logs?q=` 的过滤谓词是
//
//	rl.search_text ILIKE '%' || $n || '%'
//
// ——**前导通配符**。迁移 877 给 session_turns 家族补了 GIN **trigram** 索引，
// 让这条谓词不再对每个月分区顺序扫描（session_turns_2026_09 单分区 6.4GB，
// search_text 是 TOAST 宽列）。
//
// ★ 但 trigram 只服务「**子串**」语义：它回答「text 里有没有这段连续字符」。
//   全族没有任何 tsvector（零个 `to_tsvector` 表达式索引）⇒
//     ① 多词查询（"deepseek chat"）只能退化成「连续子串」匹配
//        （ILIKE '%deepseek chat%'），召回明显偏窄；
//     ② 没有相关性排名；
//     ③ 没有词元化。
//   迁移 887 补上承重索引：
//     USING gin (to_tsvector('simple', search_text))
//   ⇒ 查询侧必须逐字同形地写出
//     to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $n)
//   否则 planner 静默不用那条索引（**不报错**，只是顺序扫描）。
//
// # 为什么谓词形态要**决策**而不是一刀切
//
// FTS 腿与 trigram 腿的**匹配语义不同**：
//
//   · trigram / ILIKE：连续子串（"eepseek" 命中 "deepseek"）；
//   · FTS / @@：词元（"eepseek" **不**命中 "deepseek"，因为 'simple' 的
//     默认分词器把 deepseek 切成一个词元）。
//
// ⇒ 两条腿**并联（OR）**，不是串联（AND）：并联时结果是两者的并集，
//   **子串路径一条都不会丢**（"eepseek" 仍然能命中 "deepseek" 那行）。
//   串联会把子串召回砍掉 —— 那就是「破坏子串检索」。
//
// ★ 并联（OR）的**唯一代价**是 planner 侧的索引可用性：
//   顶层 OR 要求**每个参与扫描的关系**上**两条腿各自都有索引路径**，
//   否则 planner 退回顺序扫描。session_turns 家族两条腿都有
//   （877 的 trigram + 887 的 FTS）⇒ 走 BitmapOr。
//   ★ 视图 request_logs_with_current_month 的第三条 UNION 臂（v1 族
//     `_request_logs`）**没有** FTS 索引 ⇒ 那条臂会退回顺序扫描。
//     代价为零：v1 族已停写且历史已清空（2026-10-20 复核轮实测
//     `_request_logs` 行数 = 0）⇒ 那条臂扫的是空表。
//     这个前提写在迁移 887 的文件头里，将来 v1 族若回灌必须复核。
//
// # 本文件为什么是**纯函数**
//
// 判据纪律：形态判定若藏在 SQL 拼装里，就没法单独测「哪种输入走哪条腿」。
// 这里是纯函数（无 I/O、无全局状态），判据可以直接驱动它，
// 也能在变异验证里把某条腿改成不可达来证明判据真的在测东西。

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sessionSearchShape 描述一条搜索词该用哪种谓词形态。
type sessionSearchShape int

const (
	// sessionSearchSubstring：只用 trigram/ILIKE 路径。
	// 用于：非 ASCII（中文等）、过短（1-2 字符）、含路径/标点等形态。
	sessionSearchSubstring sessionSearchShape = iota
	// sessionSearchFTS：在 ILIKE 之外**并联**一条全文检索路径。
	// 用于：ASCII 词形词（可被 'simple' 分词器切成词元）且至少有一个
	// 足够长的词元。
	sessionSearchFTS
)

const (
	// sessionSearchMaxRunes 是 FTS 分支的词长上限。
	//
	// 为什么要上限：plainto_tsquery 会把整个输入切成词元，词元数与输入
	// 长度成正比；超长输入会让 tsquery 变得巨大，GIN 探测的代价随之上升，
	// 而收益（用户真去搜 200 个字符）≈ 0。
	sessionSearchMaxRunes = 200

	// sessionSearchMinTokenRunes 是「一个词元长到值得多走一条索引」的门槛。
	//
	// 为什么是 3：① 1-2 字符的词元几乎命中一切（选择性差），多走一条
	// 索引路径只是白付探测成本；② pg_trgm 的 GIN 索引对**短于 3 字符**
	// 的 LIKE 模式本来就用不上（trigram 抽不出来）⇒ 这类查询无论走哪条
	// 腿都得顺序扫，那就不必再加一条腿。
	sessionSearchMinTokenRunes = 3
)

// sessionSearchTokenExtra 是词元里允许出现的、**非字母数字**的连接符。
//
// 为什么允许 '-' 与 '.'：模型名极常见地写成 "gpt-4" / "glm-4.6"。
// 'simple' 的默认分词器把 '-' 与 '.' 当分隔符 ⇒ "gpt-4" 的 tsquery 是
// `'gpt' & '4'`。并联后它是 ILIKE 的**超集**（含 "gpt-4" 的文本必然同时
// 含词元 gpt 与 4）⇒ 只会多召回、不会少召回。
var sessionSearchTokenExtra = "-."

// classifySessionSearchTerm 决定一条搜索词该用哪种谓词形态。
//
// 返回 sessionSearchFTS 的充要条件（全部满足）：
//  1. 去空白后非空，且 rune 长度 ≤ sessionSearchMaxRunes；
//  2. 切分出的**每个**词元都由 ASCII 字母/数字/'_'/'-'/'.' 组成
//     （⇒ 含中文、含路径分隔符、含其它标点的输入一律走子串路径）；
//  3. 至少有一个词元长度 ≥ sessionSearchMinTokenRunes。
//
// ★ 为什么中文（以及任何非 ASCII）**一律**走子串路径：
//
//	'simple' 的分词器对 CJK 的切分与英文不同（连续 CJK 会被当作一个
//	词元），把它塞进 tsquery 得到的召回形状不可控；而 trigram 索引对
//	CJK 子串（≥3 字符）本来就工作良好 ⇒ 让中文留在它已经好使的那条路上。
func classifySessionSearchTerm(term string) sessionSearchShape {
	t := strings.TrimSpace(term)
	if t == "" {
		return sessionSearchSubstring
	}
	if utf8.RuneCountInString(t) > sessionSearchMaxRunes {
		return sessionSearchSubstring
	}
	tokens := strings.Fields(t)
	if len(tokens) == 0 {
		return sessionSearchSubstring
	}
	longest := 0
	for _, tok := range tokens {
		if !isSessionSearchToken(tok) {
			return sessionSearchSubstring
		}
		if n := utf8.RuneCountInString(tok); n > longest {
			longest = n
		}
	}
	if longest < sessionSearchMinTokenRunes {
		return sessionSearchSubstring
	}
	return sessionSearchFTS
}

// isSessionSearchToken 报告一个词元是否由「ASCII 字母数字 + _ + 连接符」组成。
func isSessionSearchToken(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case strings.ContainsRune(sessionSearchTokenExtra, r):
		default:
			return false
		}
	}
	return true
}

// buildSessionSearchPredicate 返回 `?q=` 过滤的 WHERE 片段与绑定参数。
//
// 返回的 clause 用 $N 占位，N 从 argIdx 起**连续**编号（argIdx 是调用方
// 当前的下一个可用占位符编号）；返回的 args 与占位符一一对应。
//
// 两种形态：
//
//	子串形态：rl.search_text ILIKE $N
//	全文形态：(rl.search_text ILIKE $N OR to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $N+1))
//
// ★ 全文形态的表达式必须与迁移 887 建的索引**逐字同形**：
//
//	`to_tsvector('simple', search_text)`。写成裸 `search_text @@ ...`、
//	或换 configuration（'english' 等）、或用 to_tsvector(text) 单参版，
//	planner 都**匹配不上**索引表达式 ⇒ 索引静默失效（不报错）。
//	硬约束：session_turns **不能**加 tsvector 生成列 ——
//	promote_session_turns_hot_to_partition 从 hot 表目录派生列清单后做
//	`INSERT INTO public.session_turns (<cols>) SELECT <cols>`，生成列会让
//	它报 cannot insert into a generated column ⇒ 热表永远排不进月分区。
//	⇒ 只能走表达式索引（见迁移 887 文件头）。
//
// ★ 参数绑定形态：ILIKE 侧绑的是「%词%」（前导通配符 ⇒ 只能靠 trigram
//
//	索引），FTS 侧绑的是**原词**（plainto_tsquery 自己会切分与加引号，
//	不能预先拼 % ）。
func buildSessionSearchPredicate(term string, argIdx int) (clause string, args []any) {
	trimmed := strings.TrimSpace(term)
	if classifySessionSearchTerm(trimmed) != sessionSearchFTS {
		return fmt.Sprintf("rl.search_text ILIKE $%d", argIdx), []any{"%" + trimmed + "%"}
	}
	return fmt.Sprintf(
			"(rl.search_text ILIKE $%d OR to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $%d))",
			argIdx, argIdx+1),
		[]any{"%" + trimmed + "%", trimmed}
}
