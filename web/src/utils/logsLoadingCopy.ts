// logsLoadingCopy.ts —— 请求日志页「加载中」文案的分档（纯函数）。
//
// ★ 为什么要有这一层（2026-11-25）：
//
//   日志列表的查询慢时，页面此前只在表格里放一行**不可见**的「加载中…」
//   （`visibility:hidden`）⇒ 用户看到的是一张空表 ⇒ 误以为「没有数据」。
//   加载态必须**看得见、说得清**：在做什么、已经等了多久、等久了该怎么办。
//
// ★ 为什么按「已等待秒数」分档，而不是按「查询参数」猜：
//
//   上一版按「含探测 && 窗口 > 6 小时」预判成「可能需要 1-2 分钟」。查询根修后
//   日级窗口是亚秒级、宽窗也只有几秒 —— 按参数预判的文案会长期谎报时长。
//   **实际等待时间**才是用户真正关心的量，也只有它不会说谎。
//
// ★ 调用点一律写**字面量键名**（不经常量转一手）：i18n 的「调用点参数 → 占位符
//   覆盖」门（callSitePlaceholderCoverage）靠静态解析键名来对拍各语种的占位符，
//   经变量间接传键会被判成「未决调用点」。
//
// ★ 文案不写在这里：词条在 locales/<lang>/requests.ts 的 `list.loading.*`
//   （8 个语种逐个定义 ⇒ 过 locale 平价门）；本函数只负责**分档**并经传入的
//   翻译函数取词 ⇒ 它自己不含硬编码文案，也便于测试。
//
// ★ 为什么放 utils：纯函数 ⇒ vitest 直接钉住各档边界（同族：
//   utils/requestLogsScanOutcome.ts、utils/latestRequestGuard.ts）。

/** 翻译函数（与 vue-i18n 的 `t` 同形；这里只要它能取词、能代入参数）。 */
export type LogsTranslate = (key: string, params?: Record<string, unknown>) => string

/** 请求日志页加载态的文案。 */
export interface LogsLoadingCopy {
  /** 主文案：正在做什么。 */
  title: string
  /** 附注：等久了才出现的处置建议（不需要时为 null）。 */
  hint: string | null
}

/** 开始显示「数据量较大」的已等待秒数。 */
export const LOGS_LOADING_SLOW_AFTER_SEC = 3

/** 开始显示处置建议的已等待秒数。 */
export const LOGS_LOADING_HINT_AFTER_SEC = 10

export function logsLoadingCopy(elapsedSeconds: number, t: LogsTranslate, withProbe = false): LogsLoadingCopy {
  // 计时器尚未走第一拍 / 负值（时钟回拨）一律当作 0 秒。
  const elapsed = Number.isFinite(elapsedSeconds) ? Math.max(0, Math.floor(elapsedSeconds)) : 0
  if (elapsed < LOGS_LOADING_SLOW_AFTER_SEC) {
    return { title: t('requests.list.loading.title'), hint: null }
  }
  if (elapsed < LOGS_LOADING_HINT_AFTER_SEC) {
    return { title: t('requests.list.loading.slow'), hint: null }
  }
  return {
    title: t('requests.list.loading.still'),
    // 业务口径：不给「关闭包含探测」这条建议（那个开关本来就没开）。
    hint: t('requests.list.loading.hint', { probe: withProbe ? t('requests.list.loading.probeSuffix') : '' }),
  }
}
