// marketPriceFormat.ts — 五价/双地板展示格式（运营定价参考共用）。
// NULL≠0：缺价一律「—」，不落 0。

export function fmtMarketNum(n: number | null | undefined, digits = 4): string {
  if (n === null || n === undefined) return '—'
  if (n >= 100) return n.toFixed(0)
  if (n >= 1) return n.toFixed(2)
  return Number(n).toFixed(digits)
}

export function fmtMarketRatio(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  return `${v.toFixed(2)}×`
}

export function fmtPair(
  a: number | null | undefined,
  b: number | null | undefined,
  sign: string,
): string {
  const cells = [
    a != null ? `${sign}${fmtMarketNum(a)}` : null,
    b != null ? `${sign}${fmtMarketNum(b)}` : null,
  ].filter((x): x is string => x != null)
  return cells.length ? cells.join(' / ') : '—'
}

export function fmtVendorAnchor(v: {
  vendor_list_currency?: string
  vendor_list_native_in_per_1m?: number | null
  vendor_list_native_out_per_1m?: number | null
  vendor_list_in_cny_per_1m?: number | null
  vendor_list_out_cny_per_1m?: number | null
}): string {
  const cny = fmtPair(v.vendor_list_in_cny_per_1m, v.vendor_list_out_cny_per_1m, '¥')
  if (v.vendor_list_currency === 'USD') {
    const native = fmtPair(v.vendor_list_native_in_per_1m, v.vendor_list_native_out_per_1m, '$')
    if (native === '—' && cny === '—') return '—'
    if (cny === '—') return native
    if (native === '—') return cny
    return `${native}（${cny}）`
  }
  return cny
}

export function fmtDualFloors(v: {
  overseas_min_in_per_1m?: number | null
  overseas_min_out_per_1m?: number | null
  overseas_min_platform_count?: number
  domestic_min_in_per_1m?: number | null
  domestic_min_out_per_1m?: number | null
  domestic_min_platform_count?: number
}): string {
  const ov = fmtPair(v.overseas_min_in_per_1m, v.overseas_min_out_per_1m, '$')
  const dm = fmtPair(v.domestic_min_in_per_1m, v.domestic_min_out_per_1m, '¥')
  return `${ov}（${v.overseas_min_platform_count ?? 0}） / ${dm}（${v.domestic_min_platform_count ?? 0}）`
}

export function marketDriftClass(v: number | null | undefined): string {
  if (v == null) return ''
  if (v > 1.5) return 'badge-red'
  if (v > 1.0) return 'badge-yellow'
  return 'badge-blue'
}
