// Guard for the audit ledger's own "pending decision" registry
// (docs/全面审计v3/00-审计覆盖台账.md).
//
// Why this guard exists (2026-10-04, audit round 232):
//
// playbook §191-B introduced a mechanical test for every registered P-level
// finding:
//
//	「它阻塞我的东西是代码里的什么位置？」
//	  → 文件:行号   ⇒ 真缺陷，可进代码修复队列
//	  → 没人决定 / 取决于产品形态 ⇒ 缺一条「承载拍板」的裁决项
//
// Applying that test to the whole queue requires ENUMERATING the queue first.
// Round 232 established that the ledger has no machine-readable item registry:
//
//   - 8 distinct written forms register an item, 4 designed to carry a number
//     (R1 「待裁决第 N 条」/ R2 「新增待裁决 N」/ R3 「【N，某号新增」/
//     R4 「（待裁决 N，」) and 4 designed not to (R5 「登记待裁决」/
//     R6 「登记「需确认是否接线」」/ R7 「（待裁决，」/ R8 「新增两条产品裁决」).
//   - 13 hits register an item with NO number, so they are invisible to any
//     numbered enumeration.
//   - 2 lines are self-contradictory registrations: the same clause says
//     「无新增待裁决条目」 and then registers one anyway.
//   - The §4.1 header self-reports a count that is not derived from the body
//     (71, while the body's largest registered number is 98 and the body only
//     enumerates 51 items).
//
// So the failure this guard prevents is concrete: a future round that
// enumerates the queue by number silently drops every numberless item, and
// reports a smaller queue with total confidence — the §157 family, where an
// absence looks exactly like a fact.
//
// Three assertions, two of which can turn red:
//
//  1. TestPendingDecisionRegistrationsCarryNumber — every numberless
//     registration must be in knownNumberlessRegistrations. Self-invalidating:
//     give that line a number and the exemption stops matching.
//  2. TestSelfContradictoryRegistrationsAreKnown — a clause that both denies
//     and registers must be in knownContradictoryLines.
//  3. TestPendingDecisionRegistryCensus — prints the form table, the numbered
//     set, the numberless list, and the three mutually inconsistent counts.
//     Print only (playbook §157: an absence is evidence, never a failure
//     condition).
//
// The clause-scoped negative detection is deliberate and load-bearing. Round
// 232's own measurement pass got this wrong four times, and a fifth time in the
// guard itself; each error produced a number that read like a conclusion:
//
//	v1  counted `待裁决 N` occurrences ⇒ that is the CROSS-REFERENCE set, not
//	    the declaration set (448 mentions / 61 numbers vs 51 registrations).
//	v2  treated 「无新增待裁决条目」 as a registration ⇒ most "registrations"
//	    were statements that nothing was registered.
//	v3  anchored on 「不擅自动手）」 ⇒ that is not a registration verb at all;
//	    it re-reported NUMBERED items (53/54/58/59/61) as numberless.
//	v4  used a ±16-character window to detect the negative form ⇒ a line like
//	    「无新增待裁决条目；新增两条产品裁决（…）」 was swallowed whole, hiding a
//	    real registration on the same line.
//	v5  (in the guard) routed every non-exempt hit through a "stale exemption"
//	    branch ⇒ negative control NC-R1 stayed GREEN on a real numberless
//	    registration. §157 is about not failing on an ABSENCE; a hit that exists
//	    and is unexempted is a positive finding and must fail.
//
// The fix for v4 is what this guard pins: split the line into clauses on
// ；/。 and test the negative form against the clause that actually contains
// the registration verb.
package sqlguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ledgerRelPath is the audit ledger, relative to the repo root.
const ledgerRelPath = "docs/\u5168\u9762\u5ba1\u8ba1v3/00-\u5ba1\u8ba1\u8986\u76d6\u53f0\u8d26.md"

type pdForm struct {
	id   string
	desc string
	re   *regexp.Regexp
	// carriesNumber records whether the form is DESIGNED to carry an item
	// number. Whether a given hit actually has one is decided by number > 0,
	// never by this flag — the two must not be conflated (draft v3 conflated
	// them and re-reported numbered items as numberless).
	carriesNumber bool
}

// pdForms are the eight written forms that register a pending decision.
//
// R2's and R5's number is an OPTIONAL capture group. Go's RE2 has no negative
// lookahead, so "carries a number" cannot be expressed as a negative match; it
// is read from the possibly-empty capture group instead.
//
// The optional group is not cosmetic: negative control NC-R1 proved that a
// required number makes the guard toothless on 「新增待裁决：」 — the shape a
// future round is most likely to write by forgetting the number.
var pdForms = []pdForm{
	{"R1", "「待裁决第 N 条」（标题或内联）", regexp.MustCompile(`待裁决第\s*(\d+)\s*条`), true},
	{"R2", "「新增待裁决 N」（编号可缺）", regexp.MustCompile(`新增\s*\**\s*待裁决\s*(?:第\s*)?(\d+)?`), true},
	{"R3", "「**【N，某号新增」", regexp.MustCompile(`\*\*【\s*(\d+)\s*[，,][^】]*号新增`), true},
	{"R4", "「（待裁决 N，」", regexp.MustCompile(`（待裁决\s*(\d+)\s*[，,]`), true},
	{"R5", "「登记待裁决」/「登记为待裁决」", regexp.MustCompile(`(?:按纪律)?登记(?:为)?待裁决\s*(?:第\s*)?(\d+)?`), false},
	{"R6", "「登记「需确认是否接线」」", regexp.MustCompile(`登记「需确认是否接线」`), false},
	{"R7", "「（待裁决，」（设计上无编号）", regexp.MustCompile(`（(?:修法方向|修法|建议|定性与建议|建议修法)?\s*待裁决\s*[，，]`), false},
	{"R8", "「新增两条产品裁决/建议」", regexp.MustCompile(`新增两条(?:产品裁决|建议|下轮必核)`), false},
}

// pdNegClause matches the ledger's way of saying "this round registered
// nothing". Deliberately NOT matched against a character window — see the
// package comment, measurement pass v4.
var pdNegClause = regexp.MustCompile(`无新增待裁决|未新增待裁决|不新增待裁决|零新增待裁决`)

// pdClauseSplit splits a line into clauses; the negative form is scoped to the
// clause that carries the registration verb.
var pdClauseSplit = regexp.MustCompile(`[；。]`)

type pdHit struct {
	form    string
	formID  string
	number  int
	line    int
	clause  string
	negated bool
}

// maskInlineCode blanks out backtick-delimited spans, replacing each with
// spaces of the SAME byte length so every offset in the returned string still
// indexes the original line.
//
// Why this exists (2026-10-04, audit round 232):
//
// this guard's own documentation — the ledger entry that introduced it — quotes
// the registration forms verbatim, inside backticks, in order to describe them:
//
//	| R5 | `登记待裁决` / `登记为待裁决` | 5 | **否** |
//
// Without masking, the guard reads its own documentation as 30 fresh
// numberless registrations and turns red on the round that created it. The
// failure is real but the cause is a category error: a form written inside a
// code span is a CITATION of a shape, not an act of registration.
//
// Blanking rather than deleting keeps the offset arithmetic in scanLedger
// valid — this bug's sibling (measurement v6) was a byte/rune mismatch in
// exactly that arithmetic.
func maskInlineCode(line string) string {
	bs := []byte(line)
	inSpan := false
	for i := 0; i < len(bs); i++ {
		switch bs[i] {
		case '`':
			inSpan = !inSpan
			bs[i] = ' '
		case '\n', '\r':
			inSpan = false
		default:
			if inSpan {
				bs[i] = ' '
			}
		}
	}
	return string(bs)
}

// scanLedger returns every registration hit, including the ones whose clause
// denies registration (negated=true), so callers can tell them apart.
func scanLedger(t *testing.T, text string) []pdHit {
	t.Helper()
	lines := strings.Split(text, "\n")
	var out []pdHit
	for i, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")
		// Scan the code-span-masked line: quoted shapes are citations, not
		// registrations. Offsets are preserved, so the clause math below is
		// unaffected.
		scan := maskInlineCode(line)
		// Clause boundaries must be computed from the separator match offsets
		// themselves. ⚠️ An earlier version accumulated `len(clause) + 1`,
		// assuming every separator is one byte — but ； and 。 are three bytes
		// in UTF-8, so the offsets drifted by two per separator and every hit
		// after the tenth was attributed to the wrong clause. That is the §183
		// family: a judge whose unit does not match the text it reads.
		clauses := pdClauseSplit.Split(scan, -1)
		var bounds [][2]int
		start := 0
		for _, s := range pdClauseSplit.FindAllStringIndex(line, -1) {
			bounds = append(bounds, [2]int{start, s[0]})
			start = s[1]
		}
		bounds = append(bounds, [2]int{start, len(line)})
		clauseAt := func(pos int) int {
			for k, b := range bounds {
				if pos < b[1] {
					return k
				}
			}
			return len(bounds) - 1
		}
		negated := make([]bool, len(clauses))
		for k, c := range clauses {
			negated[k] = pdNegClause.MatchString(c)
		}
		for _, f := range pdForms {
			locs := f.re.FindAllStringSubmatchIndex(scan, -1)
			for _, loc := range locs {
				ci := clauseAt(loc[0])
				num := 0
				if len(loc) > 2 && loc[2] >= 0 {
					// Count rhetoric is not an item number. The ledger says
					// 「新增待裁决 1 条」 to mean "one item was added" (ledger
					// L3351/L3356/L3371/L3411/L3416), which R2 would otherwise
					// read as "item number 1". The tell is the 条 right after
					// the digits with no 第 in front — 「新增待裁决第 47 条」 is a
					// real item number and must survive this filter.
					//
					// ⚠️ Trim the window before testing for 第: the ledger writes
					// 「待裁决第 37 条」 WITH a space, so the raw window is
					// "第 " and HasSuffix(…, "第") is false — which silently
					// dropped all nine legitimate R1 registrations and made the
					// enumerable count fall from 51 to 43.
					before := strings.TrimRight(scan[loc[2]-min(loc[2], 6):loc[2]], " \t")
					after := strings.TrimLeft(scan[loc[3]:], " \t")
					if strings.HasPrefix(after, "条") && !strings.HasSuffix(before, "第") {
						continue
					}
					fmt.Sscanf(scan[loc[2]:loc[3]], "%d", &num)
				}
				out = append(out, pdHit{
					form:    f.desc,
					formID:  f.id,
					number:  num,
					line:    i + 1,
					clause:  strings.TrimSpace(clauses[ci]),
					negated: negated[ci],
				})
			}
		}
	}
	return out
}

// exemptKey normalises an exemption key the same way the clause is normalised
// (code spans masked, then trimmed). Without the trim the masked key kept its
// leading blanks while the clause had already been trimmed, and the one
// exemption naming a backticked identifier silently stopped matching.
func exemptKey(k string) string {
	return strings.TrimSpace(maskInlineCode(k))
}

func readLedger(t *testing.T) string {
	t.Helper()
	root := repoRootForTest(t)
	p := filepath.Join(root, filepath.FromSlash(ledgerRelPath))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read ledger %s: %v", p, err)
	}
	return string(b)
}

// knownNumberlessRegistrations are the clauses that register a pending
// decision WITHOUT a number, as of round 232.
//
// Self-invalidating by construction: a key is matched against the clause, and
// the clause stops matching its numberless form the moment a number is added
// (「登记待裁决 99」 no longer satisfies R7; 「登记待裁决第 5 条」 no longer
// satisfies R5). When that happens the guard prints 「豁免已失效」 and stops
// counting it — it does NOT turn red, because a fixed gap is a fixed gap
// (playbook §157).
var knownNumberlessRegistrations = []string{
	"按纪律登记待裁决",
	"登记待裁决，本轮不动",
	"修法（待裁决，本轮不动）",
	"登记「需确认是否接线」",
	"`cachemetrics` 登记待裁决",
	"建议（登记为待裁决，不擅自动手）",
	"定性与建议（待裁决，不擅自动手）",
	"修法方向（待裁决，不擅自动手）",
	"新增两条建议",
	"新增两条产品裁决",
	"新增两条下轮必核",
}

// knownContradictoryLines are clauses that deny and register at the same time.
// L1448 / L1587: 「无新增待裁决条目」（X 归 B 类并登记待裁决）。
var knownContradictoryLines = []string{
	"`remotecontrol` 归 B 类并登记待裁决",
	"`orchestration/observ` 归 A/B 待裁决",
}

// TestPendingDecisionRegistrationsCarryNumber asserts that every registration
// without an item number is explicitly known.
//
// Why the number matters: §191-B's test is applied per item, and a future round
// can only apply it to items it can enumerate. A numberless registration is an
// item that silently drops out of the queue.
func TestPendingDecisionRegistrationsCarryNumber(t *testing.T) {
	text := readLedger(t)
	hits := scanLedger(t, text)

	numberless := []pdHit{}
	numbered := map[int]bool{}
	for _, h := range hits {
		if h.negated {
			continue
		}
		if h.number > 0 {
			numbered[h.number] = true
			continue
		}
		numberless = append(numberless, h)
	}

	// Anti-no-op: the scanner must actually see the ledger. A judge that
	// reports zero of everything is indistinguishable from a true absence
	// (playbook §190).
	if len(numbered) == 0 || len(numberless) == 0 {
		t.Fatalf("判据失效：带编号登记 %d 个、无编号登记 %d 处 —— 两者不可能同时为 0，"+
			"请检查形态正则是否仍匹配台账现状", len(numbered), len(numberless))
	}

	unexpected := []pdHit{}
	for _, h := range numberless {
		known := false
		for _, k := range knownNumberlessRegistrations {
			// Mask the key too: several exemptions name a backticked identifier
			// (`` `cachemetrics` 登记待裁决``) and the clause has been masked.
			if strings.Contains(h.clause, exemptKey(k)) {
				known = true
				break
			}
		}
		if known {
			continue
		}
		// A hit that exists and is unexempted is a positive finding and must
		// fail. ⚠️ An earlier draft routed these through a "stale exemption"
		// branch, on the theory that §157 says never to fail on a gap; that
		// swallowed the hit and left NC-R1 green on a real defect.
		unexpected = append(unexpected, h)
	}

	// Forward check: an exemption that no longer matches anything is stale.
	// Reported, not fatal (§157).
	for _, k := range knownNumberlessRegistrations {
		mk := exemptKey(k)
		stillThere := false
		for _, h := range numberless {
			if strings.Contains(h.clause, mk) {
				stillThere = true
				break
			}
		}
		if !stillThere {
			t.Logf("豁免已失效（该子句可能已补上编号，不再是无编号登记）：%q —— "+
				"请人工确认后从 knownNumberlessRegistrations 移除", k)
		}
	}

	if len(unexpected) > 0 {
		sort.Slice(unexpected, func(i, j int) bool { return unexpected[i].line < unexpected[j].line })
		var b strings.Builder
		b.WriteString("发现未登记的「无编号待裁决登记」：\n")
		for _, h := range unexpected {
			fmt.Fprintf(&b, "  L%d  [%s]  %s\n", h.line, h.formID, h.clause)
		}
		b.WriteString("\n每一条这样的登记都无法被按编号的枚举发现，§191-B 的判别式对它失效。\n")
		b.WriteString("处置：给它一个编号并登记进台账；或若它本就不是待裁决项，删掉这句登记动词。\n")
		t.Error(b.String())
	}

	t.Logf("无编号登记 %d 处，全部命中豁免清单（%d 条）", len(numberless), len(knownNumberlessRegistrations))
}

// TestSelfContradictoryRegistrationsAreKnown asserts that a clause which both
// denies registration and performs one is explicitly known.
//
// Shape (ledger L1448 / L1587):
//
//   - **无新增待裁决条目**（`remotecontrol` 归 B 类并登记待裁决）。
//
// Read as a queue report this says "nothing new was registered"; read as a
// registry it registers one. The same clause is both, and the clause split is
// what makes both visible at once.
func TestSelfContradictoryRegistrationsAreKnown(t *testing.T) {
	text := readLedger(t)

	lines := strings.Split(text, "\n")
	found := []string{}
	for i, raw := range lines {
		rawLine := strings.TrimSuffix(raw, "\r")
		scanLine := maskInlineCode(rawLine)
		// Detect on the masked clause (a quoted shape is a citation, not a
		// registration) but KEY on the raw clause. Masking erases exactly the
		// backticked identifier that makes a known line distinguishable, so
		// keying on the masked text would collapse 「`remotecontrol` 归 B 类并
		// 登记待裁决」 and 「`anything-else` 归 B 类并登记待裁决」 into one key
		// and make the exemption swallow the new line.
		rawClauses := pdClauseSplit.Split(rawLine, -1)
		scanClauses := pdClauseSplit.Split(scanLine, -1)
		for k := range scanClauses {
			clause := strings.TrimSpace(scanClauses[k])
			if !pdNegClause.MatchString(clause) {
				continue
			}
			if !pdForms[4].re.MatchString(clause) && !pdForms[5].re.MatchString(clause) {
				continue
			}
			key := ""
			if k < len(rawClauses) {
				key = strings.TrimSpace(rawClauses[k])
			}
			found = append(found, fmt.Sprintf("L%d: %s", i+1, key))
		}
	}

	if len(found) == 0 {
		t.Fatal("判据失效：未扫到任何「同子句既否定又登记」的行 —— " +
			"若台账确实已无此形态，请把本测试改为 knownMissing 豁免而不是保留一个恒绿的门")
	}

	unexpected := []string{}
	for _, f := range found {
		known := false
		for _, k := range knownContradictoryLines {
			if strings.Contains(f, k) {
				known = true
				break
			}
		}
		if !known {
			unexpected = append(unexpected, f)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("发现未登记的「同子句既声明无新增、又登记一条」：\n  %s\n"+
			"这类句子对队列报告是自相矛盾的：按否定读是「本轮零新增」，按登记读是「新增一条」。",
			strings.Join(unexpected, "\n  "))
	}
	for _, f := range found {
		t.Logf("已知自相矛盾登记：%s", f)
	}
}

// TestPendingDecisionRegistryCensus prints the registry's shape. Print-only on
// purpose (§157): the gaps it reports are real but unfixed, and a permanently
// red gate is worse than no gate.
func TestPendingDecisionRegistryCensus(t *testing.T) {
	text := readLedger(t)
	hits := scanLedger(t, text)

	live := []pdHit{}
	negated := 0
	for _, h := range hits {
		if h.negated {
			negated++
			continue
		}
		live = append(live, h)
	}

	byForm := map[string]int{}
	numbers := map[int]bool{}
	numberlessHits := 0
	for _, h := range live {
		byForm[h.formID]++
		if h.number > 0 {
			numbers[h.number] = true
		} else {
			numberlessHits++
		}
	}
	ns := make([]int, 0, len(numbers))
	for n := range numbers {
		ns = append(ns, n)
	}
	sort.Ints(ns)

	t.Logf("登记形态命中（含否定式 %d 处已剔除）：", negated)
	for _, f := range pdForms {
		t.Logf("  %s %-36s 命中 %3d  %s", f.id, f.desc, byForm[f.id],
			map[bool]string{true: "设计上带编号", false: "设计上无编号"}[f.carriesNumber])
	}
	t.Logf("可枚举的带编号条目 = %d 个：%v", len(ns), ns)

	// The §4.1 header self-reports a count. Report its drift instead of failing
	// on it: the header was never derived from the body, and rewriting it is a
	// ledger decision, not a code fix.
	hdr := regexp.MustCompile(`###\s*4\.1\s*待产品/运维裁决（\*\*(\d+)\s*条`)
	m := hdr.FindStringSubmatch(text)
	if m == nil {
		t.Fatal("判据失效：未匹配到 §4.1 头部自述数 —— 该断言的根据已消失，请重写本测试")
	}
	var selfReported int
	fmt.Sscanf(m[1], "%d", &selfReported)
	max := 0
	for _, n := range ns {
		if n > max {
			max = n
		}
	}
	t.Logf("§4.1 头部自述 = %d 条；台账正文可枚举条目 = %d 个；正文里最大编号 = %d",
		selfReported, len(ns), max)
	t.Logf("三个数互不相等，任何一个都不能当「台账共有多少条待裁决项」的答案：")
	t.Logf("  ① 头部自述 %d 是「编号上界」（177 号写下时最大编号就是 71），此后新增的 72~98 未回写 ⇒ 落后 %d",
		selfReported, max-selfReported)
	t.Logf("  ② 正文可枚举只有 %d 个，比自述少 %d —— 早期条目（2~36 等）登记在 2026-09-29/30 的轮次文档里，不在本台账正文",
		len(ns), selfReported-len(ns))
	t.Logf("  ③ 另有 %d 处无编号登记（见 TestPendingDecisionRegistrationsCarryNumber），按编号枚举会整批漏掉",
		numberlessHits)
}
