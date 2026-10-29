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
	out.HasExactFallback = modalityExactFallbackRE(placeholder).MatchString(sqlText)
	return out
}

// Accepts 用解析结果还原 SQL 侧对 (登记模态, 请求模态) 的裁决。
//
// ★ 忠实于文本形状：
//
//	· 请求模态为空或 'text' ⇒ 全放行（两个 SQL 抄本都有这两条全放行臂）；
//	· 命中显式臂 ⇒ 按该臂的接受集裁决（占位符展开成请求模态自身）；
//	· 未命中 ⇒ 有兜底精确臂则精确相等，否则全拒（闭世界）。
//
// ★ 空登记模态经 COALESCE 归一为 'text'（两边同形）。
func (m ModalityAcceptArms) Accepts(registered, requested string) bool {
	if registered == "" {
		registered = "text"
	}
	switch requested {
	case "", "text":
		return true
	}
	for _, a := range m.Arms {
		if a.Covers(requested) {
			return a.AcceptsFor(requested)[registered]
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
