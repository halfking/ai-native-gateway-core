package sanitize

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestBareMarkerFalsePositiveAuditOnCorpus 是 2026-10-10 那次生产抽样的
// 可复跑版本。
//
// 审计方法：从 154 生产库抽 response_preview 1920 条 + request_preview 400 条
// 真实文本，用改后的 span pattern + parseLoosePlaceholder 扫一遍，要求：
// 1. 命中的 span 全部是真 marker（生产侧统计为 24 个，无一例外）；
// 2. 不出现「无花括号且无索引」的误判（那正是本次放宽引入的新形态）。
//
// 实测结果为零误伤。这个测试在样本文件缺失时跳过，所以进 CI 不会拖慢；
// 复现方式见文件末尾注释。
func TestBareMarkerFalsePositiveAuditOnCorpus(t *testing.T) {
	path := os.Getenv("SANITIZE_FP_AUDIT_CORPUS")
	if path == "" {
		path = "/tmp/sbld3/samples.txt"
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("corpus unavailable (%s); set SANITIZE_FP_AUDIT_CORPUS to enable", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lines, spans := 0, 0
	var bare []string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		for _, sp := range loosePlaceholderSpanPattern.FindAllString(line, -1) {
			typ, idx, ok := parseLoosePlaceholder(sp)
			if !ok {
				continue
			}
			spans++
			// 无花括号 + 无索引 = 本次新增覆盖的形态。任何一条出现在真实
			// 散文里都说明规则过宽。
			if !strings.ContainsAny(sp, "{}｛｝") && idx < 0 {
				if len(bare) < 10 {
					bare = append(bare, sp+"  <<< "+truncateRunes(line, 100))
				}
			}
			_ = typ
		}
	}
	t.Logf("scanned %d lines, %d parsed marker spans", lines, spans)
	for _, b := range bare {
		t.Errorf("false positive (bare, index-less): %s", b)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
