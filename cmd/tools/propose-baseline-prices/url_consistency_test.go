// url_consistency_test.go — 快照的出处必须与三处声明**逐字一致**
//
// ★ 为什么这条必须有：基准价的权威面会把 `source_url` **冻结**进去
//
//	（buildDraft → draftLine.SourceURL → bg/data/model_baseline_prices.json）。
//	而 `source_url` 取自 `vendorPage`，**不是**取自快照自己的 `URL Source`。
//	⇒ 两者一旦不一致，SSOT 会记下一个「价是从这里读的」的地址，而这个
//	地址其实不是。这是「出处不可审计」，而且**发生在权威面上**。
//
// ── 实测（2026-10-06）：这个洞已经发生过，不是假想 ──
//
//	vendor      快照的 URL Source                          vendorPage 声明              判定
//	openai      …/docs/models                             …/docs/pricing              快照错（抓了 models 页）
//	google      ai.google.dev/pricing                     …/gemini-api/docs/pricing   两边都不是同一个
//	anthropic   …/about-claude/pricing                    …/about-claude/models/overview  映射错（快照是对的）
//
// openai 那一条的后果是可量的：models 页 47,490 字节 / **0 张表格** / 1 行含 `$`；
// pricing 页 61,525 字节 / **92 张表格** / 54 行含 `$`。
// ⇒ 抓错页 ⇒ openai 永远 0 候选，而报出来的是「提取器认不出这张表」。
//
// ── 三处声明 ──
//
//  1. raw/{vendor}.md 的 `URL Source:` 头（r.jina.ai 落地页）
//  2. `vendorPage`（本包，写进 SSOT 的 source_url）
//  3. 抓取脚本 fetch-pricing.sh 的 `fetch <name> <url>` 清单
//
// 少任何一处核，这三条就能各自漂移 —— 2026-10-06 的快照正是这么漂的。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	rawDir          = "../../../docs/02-resources/research/pricing/raw"
	fetchScriptPath = "../../../docs/02-resources/research/pricing/scripts/fetch-pricing.sh"
)

var (
	urlSourceRE = regexp.MustCompile(`(?m)^URL Source:\s*(\S+)\s*$`)
	fetchRE     = regexp.MustCompile(`(?m)^fetch\s+(\S+)\s+"([^"]+)"`)
)

// readURLSource 读快照落地页。快照由 r.jina.ai 生成，缺这个头说明抓取出问题。
func readURLSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	m := urlSourceRE.FindSubmatch(b)
	if m == nil {
		t.Fatalf("snapshot %s has no 'URL Source:' header — %d bytes, first line %q. "+
			"A snapshot without provenance cannot be checked against vendorPage, and "+
			"vendorPage's URL is what gets frozen into the SSOT's source_url.",
			path, len(b), firstLine(b))
	}
	return strings.TrimSuffix(string(m[1]), "/")
}

func firstLine(b []byte) string {
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

func fetchScriptURLs(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(fetchScriptPath)
	if err != nil {
		t.Fatalf("read fetch script: %v", err)
	}
	out := map[string]string{}
	for _, m := range fetchRE.FindAllStringSubmatch(string(b), -1) {
		out[m[1]+".md"] = strings.TrimSuffix(m[2], "/")
	}
	return out
}

// TestSnapshotURLMatchesVendorPage 是承重部分：raw/ 里每个 vendorPage 的键，
// 其快照的落地页必须**就是** vendorPage 声明的那个 URL。
func TestSnapshotURLMatchesVendorPage(t *testing.T) {
	for file, vp := range vendorPage {
		path := filepath.Join(rawDir, file)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("vendorPage declares %q but there is no snapshot at %s: %v", file, path, err)
			continue
		}
		got := readURLSource(t, path)
		want := strings.TrimSuffix(vp.URL, "/")
		if got != want {
			t.Errorf("%s: snapshot was fetched from\n     %s\nbut vendorPage (and therefore the "+
				"SSOT's source_url) claims\n     %s\nA price written into the authoritative surface "+
				"must say where it was actually read from.",
				file, got, want)
		}
	}
}

// TestFetchScriptCoversEveryVendorPage 核第三处：抓取脚本的清单必须覆盖
// vendorPage 的每一个键。少了谁，重跑抓取脚本就不会更新那个快照 —— 而
// 脚本**不会**报这件事（它对自己抓到的文件是满意的）。
func TestFetchScriptCoversEveryVendorPage(t *testing.T) {
	urls := fetchScriptURLs(t)
	if len(urls) == 0 {
		t.Fatalf("no `fetch <name> <url>` lines found in %s — the regex or the script's "+
			"shape changed, and this test is now passing for the wrong reason", fetchScriptPath)
	}
	for file, vp := range vendorPage {
		got, ok := urls[file]
		if !ok {
			t.Errorf("fetch-pricing.sh never fetches %q, but the proposal tool reads it. "+
				"Re-running the script leaves that snapshot frozen at whatever it was", file)
			continue
		}
		if want := strings.TrimSuffix(vp.URL, "/"); got != want {
			t.Errorf("fetch-pricing.sh fetches %q from %s but vendorPage says %s — running "+
				"the script would produce a snapshot that this test then rejects", file, got, want)
		}
	}
}

// openrouter.md 故意不在 vendorPage 里（聚合商价不是基准价）。这条把那个
// 「故意」钉住：有人为了让它能被提案工具读到而把它加进 vendorPage 时会红。
func TestOpenRouterStaysOutOfTheOriginatingVendorMap(t *testing.T) {
	if _, ok := vendorPage["openrouter.md"]; ok {
		t.Error("openrouter.md is in vendorPage: OpenRouter publishes RELAY prices, not " +
			"originator list prices. Putting it in the originating map mixes supplier price " +
			"into the baseline surface, which is the one thing the baseline exists to be " +
			"measured against")
	}
}

// 阴性对照：URL 只差一个字符也算不符。「基本一致」不是一致。
func TestURLComparisonIsExact(t *testing.T) {
	cases := []struct {
		got, want string
		same      bool
	}{
		{"https://a.example/p", "https://a.example/p", true},
		{"https://a.example/p/", "https://a.example/p", true}, // 结尾斜杠不算
		{"https://a.example/models", "https://a.example/pricing", false},
		{"https://a.example/pricing", "https://a.example/pricing#frag", false},
		{"http://a.example/pricing", "https://a.example/pricing", false},
		{"https://A.example/pricing", "https://a.example/pricing", false},
	}
	for _, c := range cases {
		g := strings.TrimSuffix(c.got, "/")
		w := strings.TrimSuffix(c.want, "/")
		if (g == w) != c.same {
			t.Errorf("TrimSuffix compare: got %q want %q same=%v, expected %v", c.got, c.want, g == w, c.same)
		}
	}
}
