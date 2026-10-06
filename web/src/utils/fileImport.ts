/**
 * fileImport — 文件导入的唯一校验入口（docs/UI规范 19 §4.2 C2）。
 *
 * ## 为什么要有这个文件
 *
 * HTML 规范里 `accept` 是 **"Hint for expected file type"** ——
 * **提示，不是过滤器**（[WHATWG §input](https://html.spec.whatwg.org/multipage/input.html#attr-input-accept)）。
 * 它不校验内容：用户可在系统选择器里改类型、拖拽、或直接改 DOM 绕过。
 * 本仓两个生产导入入口此前都**只靠 `accept`**，等于零校验：
 *
 *   · `views/PricingManagementView.vue` —— `onFileChange` 直接 `input.files?.[0]`
 *   · `views/AnnotationView.vue`        —— `handleImportCSV` 直接 `await file.text()`
 *
 * ## 上限数字的依据：**不是拍脑袋，是对齐服务端契约**
 *
 * C5 的纪律（同 `exportFile.ts`）：上限只能有一个权威源，且必须写明出处。
 * 本仓两个导入端点后端**已经限了流**，客户端再拍一个数只会两边打架：
 *
 * | 端点 | 服务端上限 | 出处 |
 * |---|---|---|
 * | `POST /admin/pricing/import` | 10 MiB | `admin/pricing.go:429` `r.ParseMultipartForm(10 << 20)` |
 * | `POST /api/admin/task-profile/corrections/import` | 32 MiB | `taskprofile/handler.go:350` `http.MaxBytesReader(w, r.Body, 32<<20)` |
 *
 * 客户端取**与服务端相同**的数：用户在超限时**当场**得到提示，
 * 而不是白等一次上传再收一个 4xx。
 *
 * ⚠️ **残余风险（客户端上限管不了的）**：`AnnotationView` 走
 *   `await file.text()`，32 MiB CSV 进 JS 字符串（UTF-16）约 64 MiB 内存。
 *   服务端允许 ≠ WebView 扛得住。真要收口得改成分块流式解析，
 *   本轮不越界，只把风险记在这里。
 *
 * ## 顺序：**先比体积，再比后缀**
 *
 * 与参考仓「做对的那部分」同形（`ImportView.vue` 的 `change` 双校验）。
 * 顺序有理由：体积是**唯一会让浏览器/用户白等**的那一条，先给便宜的反馈。
 *
 * ## 为什么把「读取 + 校验 + 重置 input」合成一个函数
 *
 * `PricingManagementView` 此前**没有** `input.value = ''`，
 * 而 `AnnotationView` 有。同一个动作两处写法不一致，
 * 于是「重选同一个文件不触发 change」这种 bug 只在一处出现。
 * 收进 `acceptImportFile()` 之后，**忘记重置在结构上不再可能**。
 */

/** 定价 CSV 导入上限（对齐 `admin/pricing.go:429`）。 */
export const PRICING_CSV_MAX_BYTES = 10 * 1024 * 1024 // 10 MiB

/** 标注 corrections CSV 导入上限（对齐 `taskprofile/handler.go:350`）。 */
export const CORRECTIONS_CSV_MAX_BYTES = 32 * 1024 * 1024 // 32 MiB

/** 拒绝原因。文案走 i18n（`common.importTooLarge` / `common.importBadType`），此处只给机器可读码。 */
export type ImportRejection = 'too-large' | 'bad-type'

export type ImportVerdict =
  | { ok: true }
  | { ok: false; reason: ImportRejection; maxBytes?: number; extensions?: readonly string[] }

export interface ImportRule {
  /** 字节上限。 */
  maxBytes: number
  /**
   * 允许的扩展名（含点，大小写不敏感），如 `['.csv']`。
   * 刻意是 `readonly`：规则是常量声明，调用方用 `as const` 定义时不该被迫去掉
   * 类型修饰才能传进来（vue-tsc 曾因可变数组要求直接报 TS2345）。
   */
  readonly extensions: readonly string[]
}

/**
 * 取小写扩展名；以下三种都判为**无扩展名**（返回 `''`）：
 *   · 整个名字里没有点（`README`）
 *   · 点在下标 0，即隐藏文件（`.csv`）——否则 `.gitignore` 会被当成
 *     一个名叫 `.gitignore` 的 csv 而放行
 *   · 点在末尾，后面什么都没有（`name.`）——否则会切出一个孤零零的 `.`，
 *     拿去和 `['.csv']` 比虽然不会误放行，但语义是错的
 */
export function extensionOf(filename: string): string {
  const base = filename.split(/[\\/]/).pop() ?? ''
  const dot = base.lastIndexOf('.')
  if (dot <= 0 || dot === base.length - 1) return ''
  return base.slice(dot).toLowerCase()
}

/**
 * 双校验：先体积、后后缀。返回可判定的结论，**不弹窗、不抛错** ——
 * 展示层自己决定怎么呈现（两个入口的提示位置不同：一个是内联消息，一个是顶部提示）。
 */
export function validateImportFile(
  file: { name: string; size: number } | null | undefined,
  rule: ImportRule,
): ImportVerdict {
  if (!file) return { ok: false, reason: 'bad-type', extensions: rule.extensions }
  if (file.size > rule.maxBytes) {
    return { ok: false, reason: 'too-large', maxBytes: rule.maxBytes }
  }
  const ext = extensionOf(file.name)
  if (!rule.extensions.map((e) => e.toLowerCase()).includes(ext)) {
    return { ok: false, reason: 'bad-type', extensions: rule.extensions }
  }
  return { ok: true }
}

/**
 * 拒绝原因 → 展示文案。**文案在 locales，参数在这里算**——
 * 两个入口的提示位置不同（内联消息 / 顶部提示），但「这句话怎么说」只能有一份实现。
 *
 * `t` 由调用方注入：`utils` 不该反向依赖 i18n 单例。
 */
export function importRejectionMessage(
  verdict: ImportVerdict,
  t: (key: string, params?: Record<string, unknown>) => string,
  formatBytes: (bytes?: number | null) => string,
): string {
  if (verdict.ok) return ''
  if (verdict.reason === 'too-large') {
    return t('common.importTooLarge', { max: formatBytes(verdict.maxBytes) })
  }
  return t('common.importBadType', { types: (verdict.extensions ?? []).join(' / ') })
}

/**
 * **唯一的导入文件取用入口**：读 input → 校验 → 无条件重置 `input.value`。
 *
 * 无条件重置（含「压根没有选中文件」的情况）是刻意的：
 * 重置发生在 `change` 处理的最前面，与后续分支无关，
 * 这样一个入口就不会因为某条分支忘了重置而让「重选同一文件」失效。
 */
export function acceptImportFile(
  input: HTMLInputElement,
  rule: ImportRule,
): { file: File | null; verdict: ImportVerdict } {
  const file = input.files?.[0] ?? null
  input.value = '' // 必须在校验之前无条件重置
  return { file, verdict: validateImportFile(file, rule) }
}
