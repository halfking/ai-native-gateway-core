// Package experiment 是提示词优化的**效果实验**包。
//
// 它回答一个具体问题：把提示词优化一遍，执行模型在同一批真实任务上的
// 正确率是否真的变了？
//
// 三条设计纪律（缺一条，实验结论就不可信）：
//
//  1. **判据只检查原始提示词里已经隐含的要求**。
//     判据绝不发明"原始提示词里没有"的新要求，否则测到的会是
//     "优化器+判据一起改需求"，而不是"优化器让模型更听话"。
//     每个任务的 Judge 注释里都写明它检查的是哪句原始要求。
//
//  2. **判据是规则判定，不是模型打分**。
//     用正则/JSON 解析/计数判定输出是否满足要求，可复算、无歧义，
//     不会像 LLM-as-judge 那样自证（"评估器认证的是生产没有的行为"）。
//
//  3. **正确率与"结构质量分"分开报**。
//     prompt_quality 衡量"提示词要素齐不齐"，与"任务做对没做对"无关，
//     两者混报会把"看起来更详细"误当成"效果更好"。
package experiment

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Verdict 是一次判定的结果。
type Verdict struct {
	// Passed 是否通过。
	Passed bool
	// Reason 判定理由（失败时说明缺了哪一条）。
	Reason string
}

// Task 是一个带客观判据的真实任务。
type Task struct {
	// ID 任务标识。
	ID string
	// Title 任务标题。
	Title string
	// Domain 领域（供优化器做意图分析）。
	Domain string
	// TaskType 任务类型（供优化器做意图分析）。
	TaskType string
	// Prompt 原始提示词 —— 刻意写得含糊（缺输出格式、缺显式约束）。
	Prompt string
	// Judge 判定执行模型的输出是否满足原始提示词隐含的要求。
	Judge func(output string) Verdict
}

// DefaultTasks 返回内置任务集。
//
// 六个任务覆盖六类"含糊但可判定"的真实需求：严格结构、数量、事实保留、
// 代码纯净度、格式规范、不得编造。
func DefaultTasks() []Task {
	return []Task{
		{
			ID:       "json_array_3",
			Title:    "把三条商品转成 JSON 数组",
			Domain:   "technical",
			TaskType: "json_generation",
			// 原始要求：转成 JSON 数组、三条。含糊处：输出必须**只有** JSON。
			Prompt: "把下面三条商品转成 JSON 数组给我看看：苹果 3.5、香蕉 2、橙子 4",
			Judge:  judgeJSONArray(3),
		},
		{
			ID:       "three_bullets",
			Title:    "给出 3 条数据备份建议",
			Domain:   "general",
			TaskType: "list_generation",
			// 原始要求：3 条。含糊处：没说什么算"一条"，模型常给 5 条。
			Prompt: "给我 3 条关于数据备份的建议",
			Judge:  judgeBulletCount(3),
		},
		{
			ID:       "notice_facts",
			Title:    "写一封维护通知邮件并保留事实",
			Domain:   "general",
			TaskType: "writing",
			// 原始要求：通知邮件、三个事实（周六/凌晨2点/1小时）。含糊处：模型会漏事实。
			Prompt: "写一封通知邮件，告诉大家服务器要在本周六凌晨 2 点维护，预计持续 1 小时",
			Judge: judgeContainsAll([][]string{
				{"周六", "星期六"},
				// 时间：接受同一时刻的不同书写形式。
				//
				// 判据内部一致性要求：同一组事实里，"周六/星期六"与
				// "1 小时/一小时"都接受多形态，那么时间也必须接受
				// "凌晨 2 点 / 凌晨2点 / 凌晨两点 / 凌晨 2:00 / 凌晨2:00"。
				// 否则会把**书写差异**判成**事实缺失**（实测：模型写
				// "本周六凌晨2:00至3:00"，事实完整却判负）。
				{"凌晨 2 点", "凌晨2点", "凌晨两点", "凌晨 2:00", "凌晨2:00", "2 点", "2点", "2:00", "两点"},
				{"1 小时", "1小时", "一小时", "1 个小时", "1个小时"},
			}),
		},
		{
			ID:       "python_code_only",
			Title:    "写一个挑偶数的 Python 函数",
			Domain:   "technical",
			TaskType: "code_generation",
			// 原始要求：Python 函数、挑偶数。含糊处：模型爱加"以下是…"前言。
			Prompt: "写个 Python 函数，把列表里的偶数挑出来",
			Judge:  judgePythonEvenFilter(),
		},
		{
			ID:       "date_iso",
			Title:    "按 YYYY-MM-DD 回答 100 天后日期",
			Domain:   "general",
			TaskType: "qa",
			// 原始要求：YYYY-MM-DD 格式。含糊处：模型爱写"2026年6月13日"。
			Prompt: "今天是 2026-03-05，往后推 100 天是几月几号？用 YYYY-MM-DD 格式回答",
			Judge:  judgeDateISO("2026-06-13"),
		},
		{
			ID:       "no_fabrication",
			Title:    "总结只给了一条事实的文本且不得编造",
			Domain:   "general",
			TaskType: "summarization",
			// 原始要求：总结这条文本。含糊处：模型会脑补"性能提升""安全加固"。
			Prompt: "总结这段话的核心观点：'本次发布只修复了登录页的一个样式错位问题。'",
			Judge: judgeNoFabrication(
				[]string{"登录", "样式", "错位", "修复"},
				[]string{"性能", "速度", "安全", "加密", "数据库", "崩溃", "内存", "架构", "重构", "并发", "缓存"},
			),
		},
	}
}

// ---------------------------------------------------------------------------
// 判定器
// ---------------------------------------------------------------------------

// judgeJSONArray 判定"输出整体就是一个长度为 n 的 JSON 数组"。
//
// 检查的是原始提示词里的"转成 JSON 数组"与"三条"两句。
// 判定方式刻意严格：**整个输出**（允许 ```json 代码块包裹）必须能被
// json.Unmarshal 成数组 —— 只要掺了一句"好的，这是结果："就判负。
// 这正是小模型最常见的失败形态。
func judgeJSONArray(n int) func(string) Verdict {
	return func(output string) Verdict {
		text := stripCodeFence(output)
		text = strings.TrimSpace(text)
		if text == "" {
			return Verdict{false, "输出为空"}
		}

		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(text), &arr); err != nil {
			return Verdict{false, "整个输出不是可直接解析的 JSON 数组（通常是被前言/说明文字污染）"}
		}
		if len(arr) != n {
			return Verdict{false, fmt.Sprintf("JSON 数组长度 = %d，期望 %d", len(arr), n)}
		}
		for i, item := range arr {
			var probe any
			if err := json.Unmarshal(item, &probe); err != nil {
				return Verdict{false, fmt.Sprintf("第 %d 个元素不是合法 JSON", i)}
			}
		}
		return Verdict{true, fmt.Sprintf("是长度为 %d 的 JSON 数组", n)}
	}
}

// bulletLineRe 匹配"要点行"：项目符号开头，或数字/中文数字编号开头。
var bulletLineRe = regexp.MustCompile(`^(?:[-*•·]|\(?\d+[.、)）]|[①-⑳])\s*\S`)

// judgeBulletCount 判定"要点行恰好 n 条"。
//
// 检查的是原始提示词里的"3 条"。前言行不算要点行，因此不判负 ——
// 数量不对才判负（模型给 4~6 条是最常见的违规）。
func judgeBulletCount(n int) func(string) Verdict {
	return func(output string) Verdict {
		count := 0
		for _, line := range strings.Split(output, "\n") {
			if bulletLineRe.MatchString(strings.TrimSpace(line)) {
				count++
			}
		}
		if count != n {
			return Verdict{false, fmt.Sprintf("要点行数 = %d，期望恰好 %d", count, n)}
		}
		return Verdict{true, fmt.Sprintf("要点恰好 %d 条", n)}
	}
}

// judgeContainsAll 判定"每一组同义表述里至少命中一种"。
//
// 每一组对应原始提示词里的一个事实；同义表述是为了不因"2点/2 点/两点"
// 这种书写差异判负。
func judgeContainsAll(groups [][]string) func(string) Verdict {
	return func(output string) Verdict {
		var missing []string
		for _, group := range groups {
			hit := false
			for _, alt := range group {
				if strings.Contains(output, alt) {
					hit = true
					break
				}
			}
			if !hit {
				missing = append(missing, strings.Join(group, "/"))
			}
		}
		if len(missing) > 0 {
			return Verdict{false, "缺少原始提示词中的要素: " + strings.Join(missing, ", ")}
		}
		return Verdict{true, "原始提示词的要素全部保留"}
	}
}

// preambleMarkers 是"模型加了解释性前言"的常见措辞。
var preambleMarkers = []string{
	"以下是", "这段代码", "下面的代码", "代码如下", "功能是",
	"示例代码", "解释如下", "首先，我们", "首先我们",
}

// judgePythonEvenFilter 判定"给出的是挑偶数的 Python 函数，且没有解释性前言"。
//
// 检查的是原始提示词里的"写个 Python 函数"与"把偶数挑出来"两句。
func judgePythonEvenFilter() func(string) Verdict {
	return func(output string) Verdict {
		if !strings.Contains(output, "def ") {
			return Verdict{false, "输出里没有 Python 函数定义（缺少 'def '）"}
		}
		evenMarkers := []string{"even", "% 2", "%2", "// 2", "//2", "& 1", "x % 2"}
		hit := false
		for _, m := range evenMarkers {
			if strings.Contains(output, m) {
				hit = true
				break
			}
		}
		if !hit {
			return Verdict{false, "没有识别出偶数过滤逻辑（缺少 even / %2 / //2 / &1 之一）"}
		}
		for _, marker := range preambleMarkers {
			if strings.Contains(output, marker) {
				return Verdict{false, fmt.Sprintf("输出含解释性前言 %q", marker)}
			}
		}
		return Verdict{true, "是挑偶数的 Python 函数且无前言"}
	}
}

// datePattern 匹配 YYYY-MM-DD。
var datePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// judgeDateISO 判定"输出里有期望的 YYYY-MM-DD 日期"。
//
// 检查的是原始提示词里的"用 YYYY-MM-DD 格式回答"与给定的起始日期。
func judgeDateISO(expected string) func(string) Verdict {
	return func(output string) Verdict {
		found := datePattern.FindAllString(output, -1)
		if len(found) == 0 {
			return Verdict{false, "输出里没有 YYYY-MM-DD 形式的日期"}
		}
		for _, f := range found {
			if f == expected {
				return Verdict{true, "包含期望日期 " + expected}
			}
		}
		return Verdict{false, fmt.Sprintf("日期不匹配：找到 %s，期望 %s", strings.Join(found, ","), expected)}
	}
}

// judgeNoFabrication 判定"保留了原事实，且没有编造新主题"。
//
// 检查的是原始提示词给出的那一条事实（required 里的任一表述必须出现），
// 以及"不得引入原文没有的主题"（forbidden 里的词一个都不能出现）。
func judgeNoFabrication(required, forbidden []string) func(string) Verdict {
	return func(output string) Verdict {
		hit := false
		for _, r := range required {
			if strings.Contains(output, r) {
				hit = true
				break
			}
		}
		if !hit {
			return Verdict{false, fmt.Sprintf("没有保留原文事实（%s 都没出现）", strings.Join(required, "/"))}
		}
		var extra []string
		for _, f := range forbidden {
			if strings.Contains(output, f) {
				extra = append(extra, f)
			}
		}
		if len(extra) > 0 {
			return Verdict{false, "编造了原文没有的主题: " + strings.Join(extra, ", ")}
		}
		return Verdict{true, "保留了原文事实且未编造新主题"}
	}
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// stripCodeFence 去掉包裹整段输出的 markdown 代码块。
//
// 只处理"整段被包裹"的情形；输出里混着文字时不做处理 ——
// 因为那种情况本来就该判负（"整个输出必须是 JSON"这类要求）。
func stripCodeFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	if idx := strings.IndexByte(t, '\n'); idx >= 0 {
		t = t[idx+1:]
	}
	if idx := strings.LastIndex(t, "```"); idx >= 0 {
		t = t[:idx]
	}
	return strings.TrimSpace(t)
}
