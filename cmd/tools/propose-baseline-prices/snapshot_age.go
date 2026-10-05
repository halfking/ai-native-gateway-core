package main

// 快照新鲜度：把「这份快照的内容有多旧」变成一条可判定的门。
//
// ★ 为什么需要这条门（2026-10-06 实踩）：
//
//   本工具的 `-fetched-at` 是**操作员手填**的，快照文件名是 `{vendor}.md`
//   （无日期）—— 两者之间没有任何交叉校验。于是有一次实测里，
//   快照 `raw/deepseek.md` 的**内容**对应页面 2026-06-02 的状态，而操作员
//   如实填了抓取日期 2026-08-25；本工具照单全收，产出了两条**看起来完全正常**
//   的 $0.14/$0.28 与 $0.435/$0.87。**是人在几小时后的复核里才发现的** ——
//   页面早已改成 OFF-PEAK/PEAK 峰谷分价，那两条价根本不该存在。
//
//   机制值得记：r.jina.ai 会**缓存**。缓存命中时抓取照样返回 HTTP 200、
//   内容自洽（`URL Source` 对得上、内部没有矛盾），只是**内容是旧的**。
//   ⇒ 「快照自洽」**不等于**「快照是新的」，而本工具原本只看前者。
//
// ★ 判据用快照头部 Jina 自己写的 `Published Time`，而不是文件 mtime：
//   mtime 记的是「什么时候下载的」，`Published Time` 记的是「这份内容对应的
//   页面版本是什么时候发布的」。我们要防的是**内容旧**，所以只能用后者。
//   ★ 而且 `Published Time` 缺失时**不能当作新鲜**：那是「没有任何证据」，
//   与「有证据证明它新」不是一回事 —— 见下方 noEvidence 分支。
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// publishedTimeRE 匹配快照头部的 `Published Time:` 行。
var publishedTimeRE = regexp.MustCompile(`(?m)^Published Time:[ \t]*(.+?)[ \t]*$`)

// publishedTimeLayouts 是实测到的两种写法：
//
//	r.jina.ai 输出 RFC1123（`Thu, 24 Sep 2026 09:35:27 GMT`）
//	页面自身带 RFC3339（`2026-05-27T00:00:00Z`）
var publishedTimeLayouts = []string{
	time.RFC1123Z,
	time.RFC3339,
	"Mon, 02 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05Z",
}

// parsePublishedTime 返回快照声明的内容发布时间。ok=false 表示**头里没有**。
func parsePublishedTime(raw []byte) (t time.Time, ok bool) {
	m := publishedTimeRE.FindSubmatch(raw)
	if m == nil {
		return time.Time{}, false
	}
	s := strings.TrimSpace(string(m[1]))
	for _, layout := range publishedTimeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// snapshotAgeGate 判一份快照能不能作为基准价的出处。
//
// type snapshotAgeVerdict 是三种结论，不是两种 —— 「没有证据」与「证据说它新」
// 必须分开，否则 5 份没有 `Published Time` 的快照会与 1 份 1 天前的快照
// 得到同一种待遇。
type snapshotAgeVerdict int

const (
	// ageFresh：内容发布时间距断言的抓取时间在阈值内。
	ageFresh snapshotAgeVerdict = iota
	// ageStale：头里有 `Published Time`，但内容比阈值还旧。
	ageStale
	// ageNoEvidence：头里**没有** `Published Time`，无法判断新旧。
	ageNoEvidence
)

// judgeSnapshotAge 用「断言的抓取时间」减去「页面自己的发布时间」得到内容年龄。
//
// 为什么要减去而不是直接看 Published Time：一个价格页三个月没改是完全正常的
// （静态页面），此时 Published Time 老**不代表**内容有问题。真正可疑的是
// 「你说你今天抓的，但这份内容是 84 天前的版本」—— 那指向缓存，而不是页面稳定。
// ⇒ 判据量的是**你声称的抓取时刻与内容版本之间的落差**，不是页面的绝对年龄。
func judgeSnapshotAge(raw []byte, fetchedAt time.Time, maxAgeDays float64) (snapshotAgeVerdict, time.Time, float64) {
	published, ok := parsePublishedTime(raw)
	if !ok {
		return ageNoEvidence, time.Time{}, 0
	}
	if fetchedAt.IsZero() {
		// 没有断言的抓取时间 ⇒ 算不出落差。此时不能默认「新鲜」——
		// 那正是本门要堵的那个洞（操作员填一个日期，工具就信了）。
		return ageStale, published, 0
	}
	ageDays := fetchedAt.Sub(published).Hours() / 24
	if ageDays > maxAgeDays {
		return ageStale, published, ageDays
	}
	return ageFresh, published, ageDays
}

// describeSnapshotAge 生成写给人看的结论句。**每一种结论都必须给出下一步**，
// 因为看到「跳过」而不知道该做什么的人，只会以为原厂没公布。
func describeSnapshotAge(v snapshotAgeVerdict, published time.Time, ageDays float64, name string) string {
	switch v {
	case ageStale:
		if published.IsZero() {
			return fmt.Sprintf(
				"%s: SKIPPED — no -fetched-at was given, so the age of this snapshot cannot be "+
					"cross-checked. The header of every snapshot is dated, but the tool had nothing "+
					"to compare it against and would have taken the price on trust — which is "+
					"exactly how a cached-but-consistent snapshot becomes a wrong price", name)
		}
		return fmt.Sprintf(
			"%s: SKIPPED — this snapshot's content is %.0f day(s) older than the fetch you "+
				"asserted (page Published Time %s vs fetched_at %s). r.jina.ai serves cached "+
				"renders with HTTP 200 and internally consistent content, so a stale snapshot "+
				"looks fine. Re-fetch, or pass -max-snapshot-age-days to override after "+
				"confirming the price is unchanged",
			name, ageDays, published.Format(time.RFC3339), "your -fetched-at")
	case ageNoEvidence:
		return fmt.Sprintf(
			"%s: no Published Time header — freshness cannot be proven, only assumed. "+
				"Treated as usable, but recorded here so a reader knows this price has NO "+
				"evidence of being current", name)
	default:
		return fmt.Sprintf("%s: content age %.0f day(s) — within the %s budget",
			name, ageDays, "threshold")
	}
}

// snapshotNote 是写进提案 JSON 的新鲜度结论。
//
// 三种 verdict 缺一不可：`ok` / `stale` / `no_published_time`。
// 少了第三种，「没有日期」就会被当成「新鲜」，而 10 份实测快照里有 5 份没有
// `Published Time` —— 那是**没有证据**，不是**有证据证明它新**。
type snapshotNote struct {
	File string `json:"file"`
	// Verdict: "stale" | "no_published_time"（新鲜的快照不上表，避免噪声）。
	Verdict string `json:"verdict"`
	// Published 是页面自己声明的内容发布时间（RFC3339）；没有则空。
	Published string `json:"published,omitempty"`
	// ContentAgeDays 是「断言的抓取时间 − 页面发布时间」的天数。它量的是
	// **落差**而不是页面的绝对年龄 —— 一个三个月没改的静态价格页是正常的，
	// 可疑的是「你说你今天抓的、内容却是 84 天前的版本」。
	ContentAgeDays float64 `json:"content_age_days,omitempty"`
	// Why 写清下一步动作。
	Why string `json:"why,omitempty"`
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// readSnapshotForTest 读一份真实快照。这条判据量的是**真实文件**的结论，
// 不用内联夹具 —— 因为要验的恰恰是「这些文件今天各自是什么状态」。
func readSnapshotForTest(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}
