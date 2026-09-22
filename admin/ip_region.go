package admin

// 看板 IP 地域归类（Wave 3 B7，2026-09-22 设计差距审计）。
//
// 设计差距：request_stats_dim_minute 只有 virtual_ip 维度，看板饼图全是
// 裸 IP，运营无法一眼看出"内网 vs 外网·地域"的流量分布（设计 §7 看板
// 要求内网直显 IP、外网归类国家-省-市）。
//
// 取舍（设计原文"GeoIP 库本地化，不引外网依赖"）：不引入 maxminddb 类
// 重依赖（vendor 体积 + CGO=0 约束），改为本地 CSV 段表
// data/geoip/segments.csv（行格式 `cidr,country,province,city`，运维可用
// 任何 GeoIP 工具导出；路径可用 LLM_GATEWAY_GEOIP_CSV 覆盖）。文件缺席
// 时表为空——内网判定照常，外网全部优雅降级为原样 IP 直显。
//
// 归类输出：内网/保留段 → key 原样直显（IP 即内网证据，设计原意）；
// 外网命中段表 → "国家·省·市"；外网未命中 → key 原样（降级）。

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// GeoIPCSVPathEnv overrides the default segment-table location.
const GeoIPCSVPathEnv = "LLM_GATEWAY_GEOIP_CSV"

// defaultGeoIPCSVPath is relative to the gateway working directory.
const defaultGeoIPCSVPath = "data/geoip/segments.csv"

// geoIPReloadInterval bounds staleness of the segment table; board queries
// are low-frequency so a lazy reload on read is plenty.
const geoIPReloadInterval = 10 * time.Minute

type geoSegment struct {
	prefix netip.Prefix
	label  string
}

var (
	geoIPMu        sync.Mutex
	geoIPSegments  []geoSegment
	geoIPLoadedAt  time.Time
	geoIPSourceMsg string
)

// loadGeoIPSegments reads the CSV segment table (lazy, ≤1 reload per
// geoIPReloadInterval). Absent file → empty table (graceful degradation).
func loadGeoIPSegments() []geoSegment {
	geoIPMu.Lock()
	defer geoIPMu.Unlock()
	if !geoIPLoadedAt.IsZero() && time.Since(geoIPLoadedAt) < geoIPReloadInterval {
		return geoIPSegments
	}
	path := os.Getenv(GeoIPCSVPathEnv)
	if path == "" {
		path = defaultGeoIPCSVPath
	}
	segs, err := readGeoIPCSV(path)
	if err != nil {
		geoIPSegments = nil
		geoIPSourceMsg = fmt.Sprintf("geoip csv unavailable: %v", err)
	} else {
		geoIPSegments = segs
		geoIPSourceMsg = fmt.Sprintf("geoip csv %s: %d segments", path, len(segs))
	}
	geoIPLoadedAt = time.Now()
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("geoip segment table load failed; falling back to raw IPs", "path", path, "error", err)
	}
	return geoIPSegments
}

func readGeoIPCSV(path string) ([]geoSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.Comment = '#' // `# cidr,country,...` header comments are not data
	r.FieldsPerRecord = -1 // real exports have ragged trailing columns
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	segs := make([]geoSegment, 0, len(records))
	for i, rec := range records {
		if len(rec) == 0 || strings.HasPrefix(rec[0], "#") {
			continue
		}
		// Header row tolerance: first non-comment row without a parseable
		// CIDR is skipped as a header.
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rec[0]))
		if err != nil {
			if i == 0 {
				continue
			}
			slog.Warn("geoip csv row skipped", "row", i+1, "error", err)
			continue
		}
		country, province, city := "", "", ""
		if len(rec) > 1 {
			country = strings.TrimSpace(rec[1])
		}
		if len(rec) > 2 {
			province = strings.TrimSpace(rec[2])
		}
		if len(rec) > 3 {
			city = strings.TrimSpace(rec[3])
		}
		label := strings.Join(nonEmpty(country, province, city), "·")
		if label == "" {
			continue
		}
		segs = append(segs, geoSegment{prefix: prefix.Masked(), label: label})
	}
	return segs, nil
}

func nonEmpty(vals ...string) []string {
	out := vals[:0]
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// isPrivateOrReserved reports whether the IP belongs to an intranet /
// non-routable range: RFC1918, loopback, link-local, unique-local (IPv6),
// CGNAT 100.64/10, and the documentation/benchmark ranges.
func isPrivateOrReserved(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true // treat unparsable as reserved: nothing to geo-resolve
	}
	if addr.Is4() || addr.Is4In6() {
		a := addr.Unmap()
		b := a.As4()
		switch {
		case b[0] == 10:
			return true
		case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
			return true
		case b[0] == 192 && b[1] == 168:
			return true
		case b[0] == 127:
			return true
		case b[0] == 169 && b[1] == 254:
			return true
		case b[0] == 100 && b[1] >= 64 && b[1] < 128:
			return true // CGNAT
		case b[0] == 192 && b[1] == 0 && b[2] == 2,
			b[0] == 198 && b[1] == 51 && b[2] == 100,
			b[0] == 203 && b[1] == 0 && b[2] == 113:
			return true // documentation (RFC 5737)
		case b[0] == 0:
			return true // this-network / unspecified
		}
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() ||
		(addr.Is6() && (addr.As16()[0]&0xfe) == 0xfc) // unique-local fc00::/7
}

// classifyVirtualIP maps a virtual_ip dim key to its board label:
//   - reserved/intranet addresses and the sentinel pseudo-values pass
//     through unchanged (intranet displays the IP; sentinels stay honest);
//   - public addresses resolve through the local segment table to
//     "国家·省·市";
//   - public addresses with no table hit degrade to the raw IP.
func classifyVirtualIP(raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" || strings.EqualFold(key, "unknown") || key == "-" {
		return raw
	}
	addr, err := netip.ParseAddr(key)
	if err != nil {
		// dim_key may already carry a region label (historical data) — pass
		// through untouched.
		return raw
	}
	if isPrivateOrReserved(addr) {
		return raw
	}
	for _, seg := range loadGeoIPSegments() {
		if seg.prefix.Contains(addr.Unmap()) {
			return seg.label
		}
	}
	return raw
}

// classifyVirtualIPPie remaps a virtual_ips pie's keys in place. Counts
// are preserved (one dim key maps to exactly one label; two distinct IPs
// never collide because intranet/hits/degraded all keep or replace the
// whole key — a region label can itself repeat across IPs, so equal labels
// are merged by summing).
func classifyVirtualIPPie(items []boardPieItem) []boardPieItem {
	if len(items) == 0 {
		return items
	}
	merged := make([]boardPieItem, 0, len(items))
	index := make(map[string]int, len(items))
	for _, it := range items {
		label := classifyVirtualIP(it.Key)
		if j, ok := index[label]; ok {
			merged[j].Requests += it.Requests
			merged[j].Tokens += it.Tokens
			merged[j].Credits += it.Credits
			merged[j].CostUSD += it.CostUSD
			continue
		}
		it.Key = label
		index[label] = len(merged)
		merged = append(merged, it)
	}
	return merged
}

// geoIPSourceStatus reports the current table state for /api diagnostics.
func geoIPSourceStatus() string {
	geoIPMu.Lock()
	defer geoIPMu.Unlock()
	if geoIPSourceMsg == "" {
		return "geoip csv not loaded yet"
	}
	return geoIPSourceMsg
}
