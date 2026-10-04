// titleResolver.ts — 页面标题真源优先级链（UI 规范 06 §2 + 17 §3）。
// 注册标题 > 弹层标题 > 继承快照 > 路由 titleKey > 应用名；≤200 字符。
// renderEpoch 守卫：异步解析的标题回写前必须仍属当前渲染代（R2）。

export interface TitleSource {
  /** 当前正在解析标题的 NavigationEntry 渲染代（entryId + renderEpoch）。 */
  entryId: string
  renderEpoch: number
}

export interface TitleInputs {
  /** 页面 useHyperPage 注册的动态标题（最高优先级）。 */
  registered: string | null
  /** 当前最上层弹层的显式标题。 */
  overlay: string | null
  /** 弹层无显式标题时继承的下层快照标题（06 §2 继承链）。 */
  inherited: string | null
  /** 路由静态标题（titleKey 解析结果）。 */
  route: string | null
  /** 应用名兜底。 */
  appName: string
}

const MAX_TITLE_LEN = 200

export function resolveTitle(inputs: TitleInputs): string {
  // 逐级取第一个 trim 后非空的候选（空串视为缺席，落下一级）。
  const candidates = [inputs.registered, inputs.overlay, inputs.inherited, inputs.route, inputs.appName]
  let picked = ''
  for (const c of candidates) {
    if (c !== null && c !== undefined && c.trim() !== '') {
      picked = c.trim()
      break
    }
  }
  return picked.length > MAX_TITLE_LEN ? `${picked.slice(0, MAX_TITLE_LEN - 1)}…` : picked
}

export interface RegisteredTitle {
  title: string
}

/** 页面注册表：entryId → 动态标题。useHyperPage 维护，resolver 读取。 */
export class TitleRegistry {
  private byEntry = new Map<string, string>()

  set(entryId: string, title: string): void {
    this.byEntry.set(entryId, title)
  }

  get(entryId: string): string | null {
    return this.byEntry.get(entryId) ?? null
  }

  clear(entryId: string): void {
    this.byEntry.delete(entryId)
  }
}

/** 异步标题解析守卫：解析完成时渲染代已翻则丢弃（不回写旧标题）。 */
export function shouldApplyTitle(
  claimed: TitleSource,
  current: TitleSource,
): boolean {
  return claimed.entryId === current.entryId && claimed.renderEpoch === current.renderEpoch
}
