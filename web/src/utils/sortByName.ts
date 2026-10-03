/**
 * sortByName.ts — 「有名称的列表按名称排序」的共用实现
 *
 * 2026-10-03：老板要求「对于类似供应商这类有名称的列表，需要按名称来排序，
 * 便于查找」。租户 / 用户 / 密钥 / Agent / 模块 这些列表原先都按后端返回顺序或
 * code 排，查找成本高。放在一处而不是每个页面各写一遍 —— 之前 ProvidersView
 * 自己内联了一份 localeCompare，TenantsView 又完全没有，两边行为不一致。
 *
 * 为什么不用 `a < b`：那是码点序，中文会按 Unicode 码点排（火 U+706B 排在
 * 商 U+5546 之后），看上去完全是乱的。必须用 localeCompare。
 */

/** 名称字段候选：按顺序回退，任何一行都能落到一个非空字符串上。 */
export interface NameLike {
  name?: string | null
  display_name?: string | null
  username?: string | null
  title?: string | null
  code?: string | null
  owner_user?: string | null
  key_prefix?: string | null
  /** 密钥别名（租户详情页的密钥表首列之外、人真正写下的那个名字）。 */
  key_alias?: string | null
  /**
   * 凭据的展示名（ProviderCredential.label）。**刻意不加入 DEFAULT_KEYS** ——
   * 默认候选顺序已经上线在供应商/租户/用户/密钥四处，改它会静默改变那些页面的
   * 行序。只在需要时由调用方显式传 keys 指定。
   */
  label?: string | null
  /**
   * 2026-10-03 补：模型类列表的展示名。租户模型表 / 密钥明细的模型表 /
   * 凭据可用模型面板的首列都是模型标识，之前这些表一行排序代码都没有。
   *
   * 加在 DEFAULT_KEYS **末尾**：只能影响「前面所有候选都取不到值」的行
   * （原来会沉底，现在按模型名排）。已经在用的六处列表行序**不会**变。
   */
  canonical_name?: string | null
  model?: string | null
  /** 质量表用的模型名字段（ProviderQualityData.models[].model_name）。 */
  model_name?: string | null
}

/** 取一行的可读名称，逐个回退；全空则返回空串（排序时沉底）。 */
export function nameOf(row: NameLike, keys: (keyof NameLike)[] = DEFAULT_KEYS): string {
  for (const k of keys) {
    const v = (row[k] as string | null | undefined)?.trim()
    if (v) return v
  }
  return ''
}

/** 默认候选顺序：先「显式的名字」，再退到各类标识，最后才是模型标识。 */
const DEFAULT_KEYS: (keyof NameLike)[] = [
  'name',
  'display_name',
  'username',
  'title',
  'code',
  'owner_user',
  'key_prefix',
  // 2026-10-03 追加在末尾（见 NameLike 里的说明：只影响原本会沉底的行）
  'canonical_name',
  'model',
  'model_name',
]

/**
 * 按名称字典序排序（不修改入参）。
 *
 * - localeCompare：多语言/CJK 正确序
 * - numeric: true：provider2 排在 provider10 前面（否则字典序会把 10 排在 2 前）
 * - sensitivity: 'base'：大小写不敏感，"api" 与 "API" 视为同名
 * - 空名称沉底：一行没有名字不该占住列表最前面
 * - tieBreak：同名时按稳定键（通常是 id）兜底，否则每次刷新行序会跳
 * - keys：**排序键必须与列表首列显示的主键一致**。用户页首列显示 username
 *   而 display_name 是副标题，若按默认顺序先取 display_name，就会按隐藏的
 *   中文名排（mavis_local 落到「本」= b），想找用户名反而找不到 ——
 *   与租户页那个「按 code 排、首列显示 name」是同一类毛病。
 *   所以用户页显式传 ['username']。
 */
export function sortByName<T extends NameLike>(
  rows: T[],
  tieBreak?: (a: T, b: T) => number,
  keys: (keyof NameLike)[] = DEFAULT_KEYS,
): T[] {
  return [...rows].sort((a, b) => {
    const an = nameOf(a, keys)
    const bn = nameOf(b, keys)
    if (!an && bn) return 1
    if (an && !bn) return -1
    const byName = an.localeCompare(bn, undefined, { numeric: true, sensitivity: 'base' })
    if (byName !== 0) return byName
    return tieBreak ? tieBreak(a, b) : 0
  })
}

/** 便捷版：需要按 id 兜底稳定时的常用形态。 */
export function sortByNameStable<T extends NameLike & { id: number }>(rows: T[]): T[] {
  return sortByName(rows, (a, b) => a.id - b.id)
}
