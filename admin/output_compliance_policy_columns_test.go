package admin

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutputCompliancePolicyColumnsMatchBaseline 把 outputCompliancePolicyColumns
// 里的每个裸列名钉到 baseline schema 的 output_compliance_policies 列集上。
// R27 背景：2026-07 的 security 重构把 prompt_injection_policies 的计数列名
// （total_detections/total_blocks/last_detection_at）复制进了本表的 SELECT，
// 而夹具建表跟着中毒查询走，单测全绿、生产 GET 策略配置 42703 三个月。
// 纯 mock 夹具对这类「代码×schema 漂移」免疫，只有对正典 schema 的静态
// 对账能在 CI 拦住它。
func TestOutputCompliancePolicyColumnsMatchBaseline(t *testing.T) {
	base := baselineOutputComplianceColumns(t)
	for _, line := range strings.Split(outputCompliancePolicyColumns, ",") {
		col := strings.TrimSpace(line)
		if !isBareColumnIdent(col) {
			continue // COALESCE 等表达式项及其被逗号撕裂的片段不在对账范围
		}
		if !base[col] {
			t.Errorf("outputCompliancePolicyColumns selects %q: baseline output_compliance_policies 没有这一列（疑似跨表复制列名）", col)
		}
	}
}

// isBareColumnIdent 仅认 `[a-z0-9_]+`：表达式 COALESCE(col, '{}') 按逗号
// 切开后会掉出 `'{}')` 这类碎片，必须整体排除。
func isBareColumnIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func baselineOutputComplianceColumns(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "deploy", "sql", "schemas", "baseline", "01-schema.sql")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("baseline schema 不可读: %v", err)
	}
	defer f.Close()

	const table = "output_compliance_policies"
	inBlock := false
	cols := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !inBlock {
			if strings.HasPrefix(line, "CREATE TABLE") && strings.HasSuffix(line, table+" (") {
				inBlock = true
			}
			continue
		}
		if strings.HasPrefix(line, ")") {
			return cols
		}
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "CONSTRAINT") ||
			strings.HasPrefix(line, "PRIMARY KEY") || strings.HasPrefix(line, "UNIQUE") ||
			strings.HasPrefix(line, "CHECK") || strings.HasPrefix(line, "FOREIGN") || strings.HasPrefix(line, "EXCLUDE") {
			continue
		}
		tok := line
		if i := strings.IndexAny(tok, " \t("); i >= 0 {
			tok = tok[:i]
		}
		tok = strings.Trim(tok, `"`)
		if tok != "" {
			cols[tok] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("扫描 baseline 失败: %v", err)
	}
	t.Fatalf("baseline 中未找到 CREATE TABLE ... %s（解析器失真，勿放行）", table)
	return nil
}
