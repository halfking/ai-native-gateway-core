// Diagnosis guard for audit round 241: one exemption key in the pending-decision
// registry guard collapses during normalisation and turns into a BLANKET
// exemption.
//
// The mechanism, end to end:
//
//	knownNumberlessRegistrations contains the key
//	    "`cachemetrics` 登记待裁决"
//	exemptKey(k) = strings.TrimSpace(maskInlineCode(k))
//	maskInlineCode blanks every byte BETWEEN backticks, so the identifier
//	    "cachemetrics" becomes spaces and what survives the TrimSpace is
//	    "登记待裁决"
//	the exemption is then applied with strings.Contains(clause, key)
//
// So the key stops identifying ONE clause and starts matching EVERY clause that
// contains the R5 trigger text. The registry guard's own contract is that a hit
// which exists and is unexempted is a positive finding and MUST fail (its
// package comment says so, and round 232's NC-R1 exists because an earlier
// draft swallowed such hits) — but for the R5 「登记待裁决」 spelling there is
// now nothing left to fail.
//
// Proven by two negative controls that differ by a single character:
//
//	「某某模块登记待裁决」   →  GREEN   (blind)
//	「某某模块登记为待裁决」 →  RED     (still has teeth)
//
// Both match the SAME R5 regex, `(?:按纪律)?登记(?:为)?待裁决`.
//
// ⚠️ THIS IS A TRIPWIRE ON A KNOWN DEFECT, AND IT IS DELIBERATELY GREEN TODAY.
//
// It reports the defect on every run instead of failing on it, because the fix
// (待裁决 103) requires human decisions about the ledger's content: replacing
// the degenerate key surfaces 11 currently-swallowed R5 hits, four of which
// (L5046 / L5085 / L5184 / L5289) are REAL registrations written by rounds 238,
// 239 and 240 that need either an item number or an explicit exemption.
//
// Polarity, and why it is written this way: GREEN while any key degenerates,
// RED once none does. Scanning the whole list (rather than looking for one
// named key) is what makes it hole-free — a first draft branched on "is the
// known key still in the list?" and would have stayed GREEN if someone had
// simply deleted the key, which is the most likely way the defect gets fixed.
//
// If this test goes RED, the blindness is gone. That is GOOD: apply 待裁决 103
// (replace or remove the key, then number or exempt what surfaces) and convert
// the test into a positive assertion. Do NOT just delete it — deleting it would
// restore the blindness without recording why it went away.
package sqlguard

import (
	"strings"
	"testing"
)

// degenerateExemptionKey is the key currently known to collapse. Named as a
// constant so the tripwire, the report and 待裁决 103 all point at one thing.
const degenerateExemptionKey = "`cachemetrics` 登记待裁决"

// r5TriggerProbes are two sample clauses that the R5 form must match. They are
// checked BEHAVIOURALLY against the compiled R5 regex rather than by string
// matching, so editing R5 cannot silently desynchronise this file.
var r5TriggerProbes = []string{
	"某某模块登记待裁决，明天再定",
	"某某模块登记为待裁决，明天再定",
}

func r5Form() (string, bool) {
	for _, f := range pdForms {
		if f.id == "R5" {
			return f.re.String(), true
		}
	}
	return "", false
}

// TestR5ProbesStillMatchTheR5Form keeps the probes honest: if the R5 regex stops
// matching one of them, the blindness measured below is measured against a
// probe that no longer applies, and this fails first.
func TestR5ProbesStillMatchTheR5Form(t *testing.T) {
	src, ok := r5Form()
	if !ok {
		t.Fatal("判据失效：pdForms 里没有 R5 形态")
	}
	for _, f := range pdForms {
		if f.id != "R5" {
			continue
		}
		for _, probe := range r5TriggerProbes {
			if !f.re.MatchString(probe) {
				t.Fatalf("R5 正则 %q 不再匹配探针 %q —— 下面的塌缩结论已不适用于该拼法，请重新测量", src, probe)
			}
		}
	}
	t.Logf("R5 正则 %q 对两个探针均命中（只差一个「为」字）", src)
}

// TestNoExemptionKeyCollapsesOntoAnR5Trigger scans the WHOLE list and is the
// single source of truth. It is deliberately GREEN while a degenerate key exists
// and RED once none does — so it covers BOTH fix routes (replacing the key with
// a non-collapsing one, or removing it outright) without a hole in between.
//
// Keeping it green today is the point, not an oversight: the fix (待裁决 103)
// requires human decisions about ledger content, so the defect is reported on
// every run rather than failing main unilaterally.
func TestNoExemptionKeyCollapsesOntoAnR5Trigger(t *testing.T) {
	var bad []string
	for _, k := range knownNumberlessRegistrations {
		e := exemptKey(k)
		for _, probe := range r5TriggerProbes {
			if strings.Contains(probe, e) {
				bad = append(bad, k+"  ->  "+e)
			}
		}
	}
	t.Logf("豁免键 %d 条；归一化后会命中 R5 探针（即无差别豁免）的 %d 条：%v",
		len(knownNumberlessRegistrations), len(bad), bad)

	if len(bad) == 0 {
		t.Errorf("✅ 盲区已消失：没有任何豁免键在归一化后退化成 R5 触发文本。\n\n" +
			"请接着处置 待裁决 103 的后半段——新浮出的 R5 无编号登记需要逐条决定\n" +
			"「补编号」还是「显式豁免」——然后把本测试改成正向断言\n" +
			"（即：断言这些键确实只覆盖它们各自那一条子句）。\n" +
			"**不要直接删掉本文件**：删掉就把盲区放回去了，且不留痕迹。")
		return
	}

	t.Logf("⚠️ 已知缺陷仍在（待裁决 103 未处置）：%d 条豁免键在归一化后退化成 R5 触发文本本身"+
		"（已知的一条是 %q）⇒ 任何含该文本的子句都被判为已登记，"+
		"「命中且未豁免必须失败」这条不变量对 R5「登记待裁决」拼法失效。\n"+
		"  实测后果：R5 无编号登记 13 处，其中 11 处被这条键静默吞掉；\n"+
		"  L5046 / L5085 / L5184 / L5289 是 238/239/240 三轮的真实登记，正是这道门该抓的。\n"+
		"  两臂对照：NC-I1「登记待裁决」绿 / NC-I2「登记为待裁决」红，只差一个「为」字。",
		len(bad), degenerateExemptionKey)
}
