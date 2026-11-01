package testutil

import (
	"regexp"
	"strings"
)

// 模态接受臂的【共享解析器】与【共享裁决模型】。
//
// ★ 为什么放在共享包（2026-10-29）：此前 provider 侧与 streaming 侧各有一份
// 臂解析正则（lockstepMediaArmRE / altModalityArmRE），两份正则**形态不同**
// （provider 那份只认接受侧的 `IN (...)` 形态 ⇒ 看不见 `= 'multimodal'` 那条臂；
// streaming 那份两种都认）⇒ **同一个判据被两把不同的尺量** ⇒ 尺与尺之间
// 也会漂（本轮实测：provider 的解析器对 multimodal 臂是瞎的，而两边的裁决
// 结论却必须一致 ⇒ 结论对得上纯属运气）。
// ⇒ 本文件是**唯一一份**解析器 + 裁决模型；两道锁步门都用它。
//
// ★ 裁决模型要忠实于**文本**，不能假设形状：
//   ·显式臂（请求侧带 $N）⇒ 命中即按接受集裁决；
//   · 未命中 ⇒ 看有没有【兜底精确臂】（`COALESCE(mc.modality,'text') = $N`）：
//     有 ⇒ 精确相等（开放世界）；没有 ⇒ 全拒（闭世界）。
// ⇒ 「兜底臂在不在」是文本事实，不是假设 ⇒ 有人删掉兜底臂，这里立刻变闭世界，
//   锁步门当场红（而不是靠一句注释登记）。

// ModalityAcceptArm 是一条解析出来的模态接受臂。
type ModalityAcceptArm struct {
	// Raw 是臂的原文片段（失败信息点名用）。
	Raw string
	// Reqs 是这条臂覆盖的请求模态（已归一：小写、去引号、去空白）。
	Reqs []string
	// Accepts 是接受侧的登记模态字面量集合（已归一）。
	Accepts map[string]bool
	// Excludes 是排除侧的登记模态字面量集合（已归一）。
	//
	// ★ 2026-11-02（211 生产事故 · embedding 投毒链）：文本请求臂从「全放行」
	// 收窄成「放行一切登记模态、**唯独排除 embedding**」⇒ 判据的 SQL 形态多出
	// 一种【排除臂】：接受集为空、排除集非空。
	// ⇒ 为什么不用枚举式接受集（IN ('text','vision',...)）：枚举会把「事实源
	//   新增一个登记模态」变成静默漏接（该模态当天起接不了文本请求）⇒
	//   排除式才是 forward-compatible 的形状。
	Excludes map[string]bool
	// HasPlaceholder 表示接受集里含占位符（$N）——意思是「请求模态自身」。
	// 不展开它的话，联合臂会对每个 (登记, 请求) 格假报漂移。
	HasPlaceholder bool
}

// Covers 报告这条臂是否覆盖给定的请求模态。
func (a ModalityAcceptArm) Covers(req string) bool {
	for _, r := range a.Reqs {
		if r == req {
			return true
		}
	}
	return false
}

// AcceptsFor 返回这条臂对请求值 req 的接受集（占位符展开成 req 自身）。
//
// ★ 注意：排除臂（Excludes 非空）的接受集是**空的**——「空」不等于「全拒」。
//
//	排除臂的裁决必须走 Admits（见下），不要直接读这张表。
func (a ModalityAcceptArm) AcceptsFor(req string) map[string]bool {
	s := make(map[string]bool, len(a.Accepts)+1)
	for k := range a.Accepts {
		s[k] = true
	}
	if a.HasPlaceholder {
		s[req] = true
	}
	return s
}

// Admits 报告这条臂对 (登记模态, 请求模态) 的裁决。
//
// ★ 2026-11-02：接受集与排除集分列之后，两种形状必须走**同一个入口**——
// 调用点过去直接读 `AcceptsFor(req)[reg]`，那对排除臂是错的（排除臂的
// 「接受集」是空的，空 ≠ 全拒 ⇒ 会把「排除 embedding」误判成「全拒」）。
func (a ModalityAcceptArm) Admits(registered, req string) bool {
	if len(a.Excludes) > 0 {
		// 排除臂：接一切登记模态，唯独排除排除集里的。
		return !a.Excludes[registered]
	}
	return a.AcceptsFor(req)[registered]
}

// modalityAcceptArmRE 匹配一条带接受集的显式臂：
//
//	$N IN (...) AND COALESCE(mc.modality, 'text') IN (...)
//	$N = '...' AND COALESCE(mc.modality, 'text') = '...'
//
// 组：1=请求侧 IN 列表，2=请求侧单值，3=接受侧 IN 列表，4=接受侧单值。
// COALESCE 逗号后的空格两份 SQL 抄本历史上不一致 ⇒ 用 \s* 兼容两种写法。
func modalityAcceptArmRE(placeholder string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(placeholder) +
		` (?:IN \(([^)]*)\)|= '([^']*)') AND COALESCE\(mc\.modality,\s*'text'\) (?:IN \(([^)]*)\)|= '([^']*)')`)
}

// modalityExactFallbackRE 匹配【兜底精确臂】：
//
//	COALESCE(mc.modality, 'text') = $N
//
// ★ 它没有请求侧的 $N 前缀（请求值就是占位符本身）⇒ 必须单独匹配。
func modalityExactFallbackRE(placeholder string) *regexp.Regexp {
	return regexp.MustCompile(`COALESCE\(mc\.modality,\s*'text'\)\s*=\s*` + regexp.QuoteMeta(placeholder))
}

// modalityAcceptExcludeRE 匹配一条【排除臂】：
//
//	$N = '...' AND COALESCE(mc.modality, 'text') <> '...'
//
// ★ 2026-11-02（211 生产事故）：文本请求臂就是这一形状——「接一切登记模态，
// 唯独不接 embedding」。
// ★ 与带接受集的臂**不重叠**：接受集臂的接受侧是 `IN (...)` 或 `= '...'`
//
//	⇒ `<>` 不会被 modalityAcceptArmRE 误收；排除臂的接受侧是 `<>` ⇒ 不会被
//	带接受集的臂误收。两条正则各管各的形状。
func modalityAcceptExcludeRE(placeholder string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(placeholder) +
		` = '([^']*)' AND COALESCE\(mc\.modality,\s*'text'\)\s*<>\s*'([^']*)'`)
}

// ModalityAcceptArms 是解析结果：显式臂 + 兜底臂的形态事实。
type ModalityAcceptArms struct {
	// Arms 是解析出来的显式臂（请求侧带 $N 的那些）。
	Arms []ModalityAcceptArm
	// HasExactFallback 报告文本里有没有兜底精确臂。
	HasExactFallback bool
	// Placeholder 是本次解析用的参数号（"$3" / "$5"）。
	Placeholder string
}

// ParseModalityAcceptArms 把一段 SQL 文本里的模态接受臂解析成结构化判据。
//
// placeholder 是承载请求模态的参数号（"$3" / "$5"）——两份 SQL 抄本各自用它。
func ParseModalityAcceptArms(sqlText, placeholder string) ModalityAcceptArms {
	out := ModalityAcceptArms{Placeholder: placeholder}
	armRE := modalityAcceptArmRE(placeholder)
	for _, m := range armRE.FindAllStringSubmatch(sqlText, -1) {
		arm := ModalityAcceptArm{Raw: m[0], Accepts: map[string]bool{}}
		if strings.TrimSpace(m[1]) != "" {
			arm.Reqs = append(arm.Reqs, strings.Split(m[1], ",")...)
		}
		if strings.TrimSpace(m[2]) != "" {
			arm.Reqs = append(arm.Reqs, m[2])
		}
		var accParts []string
		if strings.TrimSpace(m[3]) != "" {
			accParts = strings.Split(m[3], ",")
		} else if strings.TrimSpace(m[4]) != "" {
			accParts = []string{m[4]}
		}
		for _, p := range accParts {
			p = strings.TrimSpace(p)
			if p == placeholder {
				arm.HasPlaceholder = true
				continue
			}
			if v := strings.ToLower(strings.Trim(p, "'")); v != "" {
				arm.Accepts[v] = true
			}
		}
		for i, req := range arm.Reqs {
			arm.Reqs[i] = strings.ToLower(strings.Trim(strings.TrimSpace(req), "'"))
		}
		out.Arms = append(out.Arms, arm)
	}
	// ★ 2026-11-02（211 生产事故）：排除臂——「文本请求接一切登记模态，唯独
	//   不接 embedding」。形状与接受集臂不同（接受侧是 `<>`），单独一条正则
	//   识别；两条正则互不误收（见 modalityAcceptExcludeRE 的注释）。
	excludeRE := modalityAcceptExcludeRE(placeholder)
	for _, m := range excludeRE.FindAllStringSubmatch(sqlText, -1) {
		arm := ModalityAcceptArm{
			Raw:      m[0],
			Accepts:  map[string]bool{},
			Excludes: map[string]bool{},
		}
		arm.Reqs = []string{strings.ToLower(strings.Trim(strings.TrimSpace(m[1]), "'"))}
		if v := strings.ToLower(strings.Trim(strings.TrimSpace(m[2]), "'")); v != "" {
			arm.Excludes[v] = true
		}
		out.Arms = append(out.Arms, arm)
	}
	out.HasExactFallback = modalityExactFallbackRE(placeholder).MatchString(sqlText)
	return out
}

// Accepts 用解析结果还原 SQL 侧对 (登记模态, 请求模态) 的裁决。
//
// ★ 忠实于文本形状：
//
//	· 请求模态为空 ⇒ 全放行（两个 SQL 抄本都有这条全放行臂）；
//	· 命中显式臂 ⇒ 按该臂裁决（接受集臂按接受集；排除臂按排除集）；
//	· 未命中 ⇒ 有兜底精确臂则精确相等，否则全拒（闭世界）。
//
// ★ 空登记模态经 COALESCE 归一为 'text'（两边同形）。
//
// ★★ 2026-11-02：'text' **不再**被硬编码成全放行。文本请求臂是一条
//
//	【排除臂】（接一切登记模态，唯独不接 embedding），必须由它裁决。
//	⇒ 硬编码会把这条新判据从解析结果里抹掉 ⇒ 对拍域在 (登记=embedding,
//	  请求=text) 那一格上会**假绿**（两边都说 true）。
//	⇒ 若那条排除臂被删掉，本函数会落到兜底精确臂（registered == requested）
//	  ⇒ 与 Go 判据（embedding×text 判 false）漂移 ⇒ 锁步门当场红。
func (m ModalityAcceptArms) Accepts(registered, requested string) bool {
	if registered == "" {
		registered = "text"
	}
	if requested == "" {
		return true
	}
	for _, a := range m.Arms {
		if a.Covers(requested) {
			return a.Admits(registered, requested)
		}
	}
	if m.HasExactFallback {
		return registered == requested
	}
	return false
}

// CoversRequest 报告是否有臂覆盖该请求模态（缺臂 ⇒ 闭世界下整条列表为空）。
func (m ModalityAcceptArms) CoversRequest(requested string) bool {
	for _, a := range m.Arms {
		if a.Covers(requested) {
			return true
		}
	}
	return false
}
