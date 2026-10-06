package main

// 快照新鲜度门的判据（2026-10-06）。
//
// ★ 这条门来自一次**差一步就入账**的实测，不是来自对代码的阅读。
//
//   当时的经过：`raw/deepseek.md` 的内容对应页面 2026-06-02 的状态，而
//   操作员如实填了抓取日期 2026-08-25。本工具照单全收，产出两条看起来完全
//   正常的 $0.14/$0.28 与 $0.435/$0.87，ready_to_review 从 3 涨到 5 ——
//   而那 5 里没有一条对得上厂商当时的页面（早已改成 OFF-PEAK/PEAK 峰谷分档，
//   且我们路由的模型名已不在价目页上）。
//
//   机制是 **r.jina.ai 会缓存**：缓存命中时返回 HTTP 200、内容自洽、
//   `URL Source` 也对得上，只是**内容是旧的**。⇒ 原本只有「快照自洽」这一
//   道检查，而它恰好挡不住这类失效。
//
// 三件事必须分开，否则门会过度拒收：
//   · **内容旧**（有证据）    ⇒ 拒
//   · **没有日期**（无证据）  ⇒ 放行但记录 —— 实测 10 份快照里 5 份没有
//   · **页面很久没改但你今天抓的** ⇒ **放行** —— 静态价格页三个月不更新很正常
import (
	"strings"
	"testing"
	"time"
)

func ts(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestSnapshotAgeRefusesACachedRender(t *testing.T) {
	// 内容是 6-02 的页面；声称抓取于 8-25 ⇒ 落差 84 天，是缓存命中的形状。
	raw := []byte("Title: t\nURL Source: https://x.invalid/p\nPublished Time: Tue, 02 Jun 2026 05:31:59 GMT\n\n$0.14\n")
	v, pub, days := judgeSnapshotAge(raw, ts(2026, time.August, 25), 30)
	if v != ageStale {
		t.Errorf("verdict = %v want ageStale — an 84-day gap between the asserted fetch and the "+
			"page's own publish time is what a cached r.jina.ai render looks like, and taking "+
			"those prices is how a wrong price enters the SSOT looking normal (published=%s, %.1f days)",
			v, pub.Format(time.RFC3339), days)
	}
	msg := describeSnapshotAge(v, pub, days, "x.md")
	// 每一种结论都必须给出下一步：只说「跳过」而不说该做什么的人，
	// 只会以为「原厂没公布」。
	if !strings.Contains(msg, "Re-fetch") {
		t.Errorf("the stale verdict must tell the operator what to do next, got %q", msg)
	}
}

// ★ 过度拒收的那一半：静态价格页很久没更新**不是**问题。
//
//	门量的是「你声称的抓取时刻」与「内容版本」的**落差**，不是页面的绝对年龄。
func TestSnapshotAgeDoesNotRefuseAStaticPageFetchedPromptly(t *testing.T) {
	raw := []byte("Published Time: Tue, 02 Jun 2026 05:31:59 GMT\n\n$0.14\n")
	// 抓取于 6-05：内容 3 天前，抓取就在其后 —— 正常。
	if v, _, _ := judgeSnapshotAge(raw, ts(2026, time.June, 5), 30); v != ageFresh {
		t.Errorf("verdict = %v want ageFresh — a 3-day gap is not staleness. A gate that keyed on "+
			"the page's absolute age would refuse every static pricing page and become noise", v)
	}
	// 抓取于 9-05：同一个静态页面，落差 95 天 ⇒ 门此时**应该**响。
	// （真静态页面不会这样，但门无法区分「静态」与「缓存命中」—— 所以它只报。）
	if v, _, _ := judgeSnapshotAge(raw, ts(2026, time.September, 5), 30); v != ageStale {
		t.Errorf("verdict = %v want ageStale — 95 days between the asserted fetch and the content "+
			"version must be reported even though the page may well be genuinely static", v)
	}
}

// 「没有日期」是**无证据**，不是**有证据证明它新**。两者必须分开。
func TestSnapshotAgeDistinguishesNoEvidenceFromFresh(t *testing.T) {
	raw := []byte("Title: t\nURL Source: https://x.invalid/p\n\n$0.14\n") // 没有 Published Time
	v, _, _ := judgeSnapshotAge(raw, ts(2026, time.October, 5), 30)
	if v != ageNoEvidence {
		t.Fatalf("verdict = %v want ageNoEvidence — 5 of the 10 real snapshots carry no "+
			"Published Time header at all; calling those \"fresh\" is asserting something the "+
			"tool cannot know", v)
	}
	msg := describeSnapshotAge(v, time.Time{}, 0, "x.md")
	if !strings.Contains(msg, "cannot be proven") {
		t.Errorf("the no-evidence message must say the freshness is unproven, got %q", msg)
	}
}

// 没有断言抓取时间时**不得默认放行** —— 那正是本门要堵的洞。
func TestSnapshotAgeRefusesWhenNoFetchIsAsserted(t *testing.T) {
	raw := []byte("Published Time: Tue, 02 Jun 2026 05:31:59 GMT\n\n$0.14\n")
	if v, _, _ := judgeSnapshotAge(raw, time.Time{}, 30); v != ageStale {
		t.Errorf("verdict = %v want ageStale — with no -fetched-at the tool has nothing to "+
			"cross-check the snapshot's date against, and \"trust the operator's price on "+
			"trust\" is the failure this whole gate exists to stop", v)
	}
	// ⚠ 第一版这里断言 ageStale，是**我写错了**：无日期时正确答案是
	//   ageNoEvidence。「没有日期」是更根本的事实 —— 抱怨「你没填抓取时间」
	//   是次要的，而且会让人困惑（他确实填了，是页面根本没给日期）。
	if v, _, _ := judgeSnapshotAge([]byte("no header\n"), time.Time{}, 30); v != ageNoEvidence {
		t.Errorf("verdict = %v want ageNoEvidence — with neither a header nor an asserted fetch, "+
			"the primary fact is that the snapshot carries no date at all", v)
	}
}

// 两种 Published Time 写法都要认：r.jina.ai 输出 RFC1123，页面自身带 RFC3339。
func TestPublishedTimeParsesBothRealWorldFormats(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"rfc1123-from-jina", "Thu, 24 Sep 2026 09:35:27 GMT", "2026-09-24T09:35:27Z"},
		{"rfc3339-from-page", "2026-05-27T00:00:00Z", "2026-05-27T00:00:00Z"},
	} {
		raw := []byte("Title: t\nPublished Time: " + tc.header + "\n\nbody\n")
		got, ok := parsePublishedTime(raw)
		if !ok {
			t.Errorf("%s: parsePublishedTime returned ok=false; a header format we fail to "+
				"parse degrades silently into ageNoEvidence, which looks like \"no date\" rather "+
				"than \"we could not read the date\"", tc.name)
			continue
		}
		if fmtTime(got) != tc.want {
			t.Errorf("%s: parsed %s want %s", tc.name, fmtTime(got), tc.want)
		}
	}
}

// 量具自证 + 真实快照的结论必须与实测一致。
//
// 实测（2026-10-06 重抓全部快照后，fetched_at = 当天，阈值 30 天）。
// ★ 上一版（2026-10-05）的表按**更早**的快照写，xai/zhipu 当时是 5 月与 6 月
// 的内容版本。2026-10-06 重跑 fetch-pricing.sh 后逐份读头：
//
//	xai.md          2026-09-29  落差  6 天  ⇒ fresh
//	zhipu.md        2026-09-24  落差 11 天  ⇒ fresh
//	google-gemini.md 2026-10-01 落差  4 天  ⇒ fresh
//	openai.md       2026-10-06  落差  0 天  ⇒ fresh
//	deepseek.md     2026-09-24  落差 11 天  ⇒ fresh
//	其余 4 份无 Published Time                  ⇒ no_evidence
//
// ⚠ 与本表配套的一条实测（很重要，别照抄「Published Time = 页面发布时间」
// 的想当然）：`Published Time` 是 **r.jina.ai 缓存渲染的产物**。带
// `x-no-cache: true` 重抓同一页，**整行消失**，而正文逐字相同（只差一个空行）。
// 对同一页连续抓两次，PT 也可能逐字不变（deepseek 两次都是 09:35:27）。
// ⇒ 它量的是「这份缓存内容是什么时候被渲染出来的」，重抓一次即归零，
// 所以**它证明不了页面被更新过**。本表因此只能核「门对每份文件给出什么结论」，
// 不足以核「价是最新的」—— 后者要靠互证（-corroborate）与人工比对。
func TestRealSnapshotsGetTheFreshnessVerdictTheyEarn(t *testing.T) {
	dir := "../../../docs/02-resources/research/pricing/raw"
	fetched := ts(2026, time.October, 5)
	want := map[string]snapshotAgeVerdict{
		"xai.md": ageFresh, "zhipu.md": ageFresh, "google-gemini.md": ageFresh,
		"openai.md": ageFresh, "deepseek.md": ageFresh,
		"anthropic.md": ageNoEvidence, "doubao.md": ageNoEvidence,
		"MiniMax-paygo.md": ageNoEvidence,
		"mistral.md":       ageNoEvidence,
	}
	seen := map[string]snapshotAgeVerdict{}
	for name := range want {
		raw := readSnapshotForTest(t, dir, name)
		v, _, _ := judgeSnapshotAge(raw, fetched, 30)
		seen[name] = v
	}
	for name, wantV := range want {
		got, ok := seen[name]
		if !ok {
			t.Errorf("%s was not examined — the assertion would be vacuous", name)
			continue
		}
		if got != wantV {
			names := map[snapshotAgeVerdict]string{ageFresh: "fresh", ageStale: "stale", ageNoEvidence: "no_evidence"}
			t.Errorf("%s: verdict = %s want %s. Either a snapshot went stale without being "+
				"re-fetched, or the header format regressed. Do NOT just update this table: "+
				"check whether the price is still what the vendor publishes",
				name, names[got], names[wantV])
		}
	}
}
