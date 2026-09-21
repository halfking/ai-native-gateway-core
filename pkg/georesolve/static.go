package georesolve

import "net"

// defaultStaticRanges returns the offline CN ISP / regional table. The
// entries are sourced from the APNIC delegated stats plus the published
// CN ISP allocations; values are coarse (country + province + ISP) —
// city-level resolution requires a real GeoIP database and is intentionally
// left to a future Backend implementation.
//
// Keep this table small and curated. Each entry is one APNIC-allocated
// block; full coverage would be tens of thousands of CIDRs and is better
// served by an mmdb file.
func defaultStaticRanges() []staticRange {
	return []staticRange{
		// ─── China Telecom (CHINANET) ───────────────────────────────────────
		{
			cidr: mustCIDR("36.0.0.0/12"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("59.32.0.0/13"),
			loc:  Location{Country: "CN", Region: "Guangdong", City: "Guangzhou", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("59.108.0.0/15"),
			loc:  Location{Country: "CN", Region: "Beijing", City: "Beijing", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("61.135.0.0/16"),
			loc:  Location{Country: "CN", Region: "Beijing", City: "Beijing", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("101.226.0.0/15"),
			loc:  Location{Country: "CN", Region: "Shanghai", City: "Shanghai", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("116.236.0.0/14"),
			loc:  Location{Country: "CN", Region: "Shanghai", City: "Shanghai", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("180.97.0.0/16"),
			loc:  Location{Country: "CN", Region: "Jiangsu", City: "Nanjing", ISP: "China Telecom"},
		},
		{
			cidr: mustCIDR("222.215.0.0/16"),
			loc:  Location{Country: "CN", Region: "Sichuan", City: "Chengdu", ISP: "China Telecom"},
		},

		// ─── China Unicom (CNCNET / China169-Backbone) ─────────────────────
		{
			cidr: mustCIDR("58.16.0.0/14"),
			loc:  Location{Country: "CN", Region: "Guizhou", City: "Guiyang", ISP: "China Unicom"},
		},
		{
			cidr: mustCIDR("110.242.0.0/15"),
			loc:  Location{Country: "CN", Region: "Hebei", City: "Shijiazhuang", ISP: "China Unicom"},
		},
		{
			cidr: mustCIDR("111.0.0.0/12"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Unicom"},
		},
		{
			cidr: mustCIDR("123.112.0.0/12"),
			loc:  Location{Country: "CN", Region: "Beijing", City: "Beijing", ISP: "China Unicom"},
		},
		{
			cidr: mustCIDR("210.22.0.0/15"),
			loc:  Location{Country: "CN", Region: "Shanghai", City: "Shanghai", ISP: "China Unicom"},
		},
		{
			cidr: mustCIDR("221.0.0.0/13"),
			loc:  Location{Country: "CN", Region: "Shandong", City: "Jinan", ISP: "China Unicom"},
		},

		// ─── China Mobile (CMNET) ───────────────────────────────────────────
		{
			cidr: mustCIDR("36.128.0.0/10"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Mobile"},
		},
		{
			cidr: mustCIDR("39.134.0.0/15"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Mobile"},
		},
		{
			cidr: mustCIDR("120.196.0.0/13"),
			loc:  Location{Country: "CN", Region: "Guangdong", City: "Shenzhen", ISP: "China Mobile"},
		},
		{
			cidr: mustCIDR("183.224.0.0/12"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Mobile"},
		},
		{
			cidr: mustCIDR("223.64.0.0/11"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "China Mobile"},
		},

		// ─── China Education / CERNET ───────────────────────────────────────
		{
			cidr: mustCIDR("58.192.0.0/11"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "CERNET"},
		},
		{
			cidr: mustCIDR("166.111.0.0/16"),
			loc:  Location{Country: "CN", Region: "Beijing", City: "Beijing", ISP: "CERNET"},
		},

		// ─── China Science / CSTNET ─────────────────────────────────────────
		{
			cidr: mustCIDR("159.226.0.0/16"),
			loc:  Location{Country: "CN", Region: "Beijing", City: "Beijing", ISP: "CSTNET"},
		},

		// ─── Great Wall Broadband (GWBN / Dr.Peng) ──────────────────────────
		{
			cidr: mustCIDR("124.192.0.0/14"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "Great Wall Broadband"},
		},
		{
			cidr: mustCIDR("219.234.0.0/16"),
			loc:  Location{Country: "CN", Region: "Multi", ISP: "Great Wall Broadband"},
		},

		// ─── Hong Kong / Singapore / JP / US coarse buckets ─────────────────
		// Operators see a lot of cloud traffic from these regions. We bucket
		// the major cloud-provider ranges so the dashboard shows them as a
		// known external region instead of "ZZ".
		{
			cidr: mustCIDR("13.105.0.0/16"),
			loc:  Location{Country: "US", Region: "Multi", ISP: "Microsoft Azure"},
		},
		{
			cidr: mustCIDR("20.0.0.0/8"),
			loc:  Location{Country: "US", Region: "Multi", ISP: "Microsoft Azure"},
		},
		{
			cidr: mustCIDR("52.0.0.0/8"),
			loc:  Location{Country: "US", Region: "Multi", ISP: "Amazon AWS"},
		},
		{
			cidr: mustCIDR("104.16.0.0/12"),
			loc:  Location{Country: "US", Region: "Multi", ISP: "Cloudflare"},
		},
		{
			cidr: mustCIDR("172.64.0.0/13"),
			loc:  Location{Country: "US", Region: "Multi", ISP: "Cloudflare"},
		},
		{
			cidr: mustCIDR("8.8.8.0/24"),
			loc:  Location{Country: "US", Region: "California", City: "Mountain View", ISP: "Google"},
		},
		// HK common allocations
		{
			cidr: mustCIDR("47.75.0.0/16"),
			loc:  Location{Country: "CN", Region: "Hong Kong", City: "Hong Kong", ISP: "Alibaba Cloud"},
		},
		{
			cidr: mustCIDR("47.91.0.0/16"),
			loc:  Location{Country: "CN", Region: "Hong Kong", City: "Hong Kong", ISP: "Alibaba Cloud"},
		},
	}
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		// Hard-fail at init: an entry with a malformed CIDR is a programmer
		// error, not a runtime condition.
		panic("georesolve: invalid static CIDR " + s + ": " + err.Error())
	}
	return n
}