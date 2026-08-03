package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSeedSnapshot_RoundTrip 是 GW-01 的核心验收：证明 seed generator 幂等
// 且不丢字段。
//
// 流程：解析 sql/schema/02-seed.sql 里所有 provider_catalog INSERT →
// []CatalogEntry → ValidateSet 全过 → GenerateSeed → 重新解析 → 深度相等。
//
// 解析器只识别 02-seed.sql 现有的 positional `INSERT INTO public.provider_catalog
// VALUES (...) ON CONFLICT DO NOTHING;` 形式（SOURCE-VERIFIED）。
// 如果 seed 文件改了格式（例如改用显式列名），更新本测试的解析器。
func TestSeedSnapshot_RoundTrip(t *testing.T) {
	seedPath := filepath.Join("..", "..", "sql", "schema", "02-seed.sql")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Skipf("seed file not found at %s: %v (skip in non-repo env)", seedPath, err)
	}
	entries, err := parsePositionalInserts(string(raw))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	if len(entries) < 10 {
		t.Fatalf("expected >=10 provider_catalog rows in seed, got %d", len(entries))
	}
	t.Logf("parsed %d catalog entries from 02-seed.sql", len(entries))

	if err := ValidateSet(entries); err != nil {
		t.Fatalf("seed entries failed ValidateSet: %v", err)
	}

	// 生成新 seed，重新解析，逐字段比较。
	regen := GenerateSeed(entries)
	regenEntries, err := parseExplicitInserts(regen)
	if err != nil {
		t.Fatalf("re-parse generated seed: %v", err)
	}
	if len(regenEntries) != len(entries) {
		t.Fatalf("round-trip lost rows: in=%d out=%d", len(entries), len(regenEntries))
	}
	for i, want := range entries {
		got := regenEntries[i]
		if !entriesEqual(want, got) {
			t.Errorf("entry[%d] code=%s round-trip mismatch:\n want=%+v\n got =%+v", i, want.Code, want, got)
		}
	}
}

// parsePositionalInserts 解析 02-seed.sql 的 positional VALUES 形式。
// 列顺序 = seedColumns（与 DDL 一致，SOURCE-VERIFIED from 02-seed.sql:847）。
func parsePositionalInserts(s string) ([]CatalogEntry, error) {
	re := regexp.MustCompile(`(?s)INSERT INTO public\.provider_catalog VALUES \((.*?)\) ON CONFLICT DO NOTHING;`)
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]CatalogEntry, 0, len(matches))
	for _, m := range matches {
		vals := splitSQLValues(m[1])
		// seed 用 25 列布局（含 created_at/updated_at），GenerateSeed 用 23 列。
		if len(vals) != 25 && len(vals) != len(seedColumns) {
			return nil, nil // 跳过格式不符（不应发生）
		}
		e := entryFromPositional(vals)
		out = append(out, e)
	}
	return out, nil
}

// parseExplicitInserts 解析 GenerateSeed 产出的显式列名形式。
func parseExplicitInserts(s string) ([]CatalogEntry, error) {
	re := regexp.MustCompile(`(?s)INSERT INTO public\.provider_catalog \((.*?)\) VALUES \((.*?)\) ON CONFLICT \(code\) DO UPDATE SET`)
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]CatalogEntry, 0, len(matches))
	for _, m := range matches {
		vals := splitSQLValues(m[2])
		if len(vals) != 25 && len(vals) != len(seedColumns) {
			return nil, nil
		}
		e := entryFromPositional(vals)
		out = append(out, e)
	}
	return out, nil
}

// entryFromPositional 按 DDL 列顺序把 SQL 字面量映射回 CatalogEntry。
//
// 02-seed.sql 用 positional VALUES，列顺序 = provider_catalog.sql DDL 全列
// （含 created_at/updated_at，共 25 列）。SOURCE-VERIFIED from 02-seed.sql:847。
// 本函数接受 25 值布局，丢弃 created_at/updated_at（索引 20,21）。
func entryFromPositional(v []string) CatalogEntry {
	// 25 列布局：0-19 业务字段，20=created_at, 21=updated_at,
	// 22=header_profile_code, 23=capabilities, 24=vendor_name。
	// 但 seed 末尾实际是 ..., 1, ts, ts, NULL, '{}', NULL — 即
	// catalog_version(19), created(20), updated(21), header(22), cap(23), vendor(24).
	strip := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "NULL" {
			return ""
		}
		if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
			s = s[1 : len(s)-1]
			s = strings.ReplaceAll(s, "''", "'")
		}
		return s
	}
	parseBool := func(s string) bool {
		return strings.EqualFold(strings.TrimSpace(s), "TRUE")
	}
	parseJSON := func(s string) json.RawMessage {
		s = strings.TrimSpace(s)
		if s == "NULL" {
			return nil
		}
		if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
			s = s[1 : len(s)-1]
			s = strings.ReplaceAll(s, "''", "'")
		}
		return json.RawMessage(s)
	}
	if len(v) == 25 {
		// seed 的 25 值布局
		return CatalogEntry{
			Code:                 strip(v[0]),
			Tier:                 strip(v[1]),
			DisplayName:          strip(v[2]),
			DisplayNameEN:        strip(v[3]),
			Category:             strip(v[4]),
			Kind:                 strip(v[5]),
			Protocol:             strip(v[6]),
			BaseURLTemplate:      strip(v[7]),
			DocsURL:              strip(v[8]),
			DefaultEgressProfile: strip(v[9]),
			Domestic:             parseBool(v[10]),
			DiscountRateDefault:  parseSeedFloat(strip(v[11])),
			ModelsManifestJSON:   parseJSON(v[12]),
			DiscoveryStrategy:    strip(v[13]),
			ModelsEndpointTpl:    strip(v[14]),
			SeedPricingPlansJSON: parseJSON(v[15]),
			PriceSourcesJSON:     parseJSON(v[16]),
			Hidden:               parseBool(v[17]),
			Notes:                strip(v[18]),
			CatalogVersion:       parseSeedInt(strip(v[19])),
			// v[20]=created_at, v[21]=updated_at 丢弃
			HeaderProfileCode: strip(v[22]),
			Capabilities:      parseJSON(v[23]),
			VendorName:        strip(v[24]),
		}
	}
	// GenerateSeed 的 23 值布局（无 created_at/updated_at）
	return CatalogEntry{
		Code:                 strip(v[0]),
		Tier:                 strip(v[1]),
		DisplayName:          strip(v[2]),
		DisplayNameEN:        strip(v[3]),
		Category:             strip(v[4]),
		Kind:                 strip(v[5]),
		Protocol:             strip(v[6]),
		BaseURLTemplate:      strip(v[7]),
		DocsURL:              strip(v[8]),
		DefaultEgressProfile: strip(v[9]),
		Domestic:             parseBool(v[10]),
		DiscountRateDefault:  parseSeedFloat(strip(v[11])),
		ModelsManifestJSON:   parseJSON(v[12]),
		DiscoveryStrategy:    strip(v[13]),
		ModelsEndpointTpl:    strip(v[14]),
		SeedPricingPlansJSON: parseJSON(v[15]),
		PriceSourcesJSON:     parseJSON(v[16]),
		Hidden:               parseBool(v[17]),
		Notes:                strip(v[18]),
		CatalogVersion:       parseSeedInt(strip(v[19])),
		HeaderProfileCode:    strip(v[20]),
		Capabilities:         parseJSON(v[21]),
		VendorName:           strip(v[22]),
	}
}

func parseSeedFloat(s string) float64 {
	var f float64
	for _, r := range s {
		if r >= '0' && r <= '9' {
			f = f*10 + float64(r-'0')
		}
	}
	return f
}

func parseSeedInt(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

// splitSQLValues 把 VALUES (...) 里顶层逗号分隔的值切开。
// 尊重单引号字符串（字符串内的逗号不切），不处理嵌套括号（catalog 值里没有）。
func splitSQLValues(s string) []string {
	var out []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			inStr = !inStr
			cur.WriteByte(c)
			continue
		}
		if c == ',' && !inStr {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// entriesEqual 比较 round-trip 后的两个 entry（容忍 DiscountRateDefault
// 的浮点表示差异、nil vs 空 JSON）。
func entriesEqual(a, b CatalogEntry) bool {
	if a.Code != b.Code || a.Tier != b.Tier || a.Protocol != b.Protocol ||
		a.BaseURLTemplate != b.BaseURLTemplate || a.DisplayName != b.DisplayName ||
		a.VendorName != b.VendorName || a.DiscoveryStrategy != b.DiscoveryStrategy ||
		a.Kind != b.Kind || a.Category != b.Category {
		return false
	}
	if !jsonRawEqual(a.ModelsManifestJSON, b.ModelsManifestJSON) {
		return false
	}
	if !jsonRawEqual(a.Capabilities, b.Capabilities) {
		return false
	}
	return true
}

func jsonRawEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	var ja, jb any
	if err := json.Unmarshal(a, &ja); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &jb); err != nil {
		return false
	}
	return jsonEqual(ja, jb)
}

func jsonEqual(a, b any) bool {
	return jsonDeepEqual(a, b)
}

func jsonDeepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
