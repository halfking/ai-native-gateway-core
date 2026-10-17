// liveTopologyLayout.ts — 拓扑布局（纯函数，2026-10-17 改版：上→下 + 网关核心整体）
//
// 四层自上而下：客户端 → **网关核心大方块** → 供应端节点 → 模型。
// 网关核心不是一个圆点，而是横跨中段的一个大矩形，内部 3×3 排布九个处理模块
// （缓存/脱敏/路由/节点状态/会话文件/会话写入/错误记录/探测任务/任务识别），
// 主链路模块实线、旁路模块虚线（承 GATEWAY_BRANCH_MODULES 的语义）。
//
// **必须确定性**：同输入同输出 —— 每次刷新节点乱跳，运维没法形成肌肉记忆，
// 且所有「节点在第几行」的断言失效。抖动来自 id 的稳定哈希，不用 Math.random()。

import {
  GATEWAY_MODULES,
  TOPO_COLUMNS,
  type GatewayCoreBox,
  type TopoColumn,
  type TopoEdge,
  type TopoLayout,
  type TopoLayoutEdge,
  type TopoLayoutNode,
  type TopoNode,
} from '../types/liveTopology'

export interface LayoutInput {
  nodes: readonly TopoNode[]
  edges: readonly TopoEdge[]
  width: number
  height: number
}

/** 节点卡尺寸。导出给判据用：重叠这类断言必须与布局共享同一份尺寸，
 *  否则测试里写死一个数字，布局一改就变成「测的不是它」。 */
/** 节点卡**最大**尺寸（导出给判据用：重叠/越界这类断言必须与布局共享同一份数字，
 *  否则测试里写死一个值，布局一改就变成「测的不是它」）。 */
export const NODE_W = 150
export const NODE_H = 42
/** 窄屏下卡片收缩到这个宽度以下就不再缩了（再窄文字就不可读了）。 */
export const NODE_W_MIN = 104
/** 卡片之间至少留这么宽，否则视觉上就是「挤成一坨」。 */
export const NODE_GAP = 12
/** 画布左右各留的白边。 */
const SIDE_PAD = 12

/**
 * 布局宽度的**下限**，只用于兜住「容器为 0」这种退化输入。
 *
 * 早先这里是 640，于是窄屏（容器 456px）时布局仍按 640 排布，而
 * `.topo-canvas` 是 `overflow:hidden` —— 右侧 113px 直接被裁掉，
 * 大方块右边和每行最后几张卡永远看不见。**下限不能大于容器**，
 * 否则「多画出来的那部分」不是信息，是丢信息。
 */
export const MIN_LAYOUT_WIDTH = 320
export const MIN_LAYOUT_HEIGHT = 480

/**
 * 该宽度下能摊下多少张卡，**以及每张卡该有多宽**。
 *
 * 为什么必须一起算：卡宽固定 150 时，窄屏（456px 容器）连两张都放不下 ——
 * 槽位 141px < 需要的 162px。单纯「少画一张」解决不了：一张也会占 150px
 * 挡住旁边。所以窄屏要么让卡片变窄，要么改用纵向堆叠。前者更简单，
 * 且卡片上只有名称/副标题/计数，变窄后信息量不受影响（文字过长的走 ellipsis）。
 *
 * 返回 `visible` = 放得下的可见卡数（调用方再决定是否折叠剩下的）。
 */
export function fitRow(width: number, reservingFoldSlot: boolean): { visible: number; cardW: number; foldable: boolean } {
  // **先问「几张能放下满宽卡」**（inner = cards*cardW + (cards+1)*GAP），
  // 这个数就是我们要的 —— 卡片保持 150 可读，摊不下的才进折叠。
  // 只有连两张满宽卡都放不下时，才退到「窄卡多摆几张」。
  //
  // 早先直接从 maxCards=inner/NODE_W_MIN 往下枚举，于是 1440 被摊成
  // 12 张 105px 的窄卡 —— 张数上去了，可读性全丢，正是「多画了但看不见」。
  // **取舍原则：卡宽可读优先于张数。**
  const inner = Math.max(0, width - SIDE_PAD * 2)
  /**
   * 槽位模型：`inner = cards*cardW + (cards+1)*GAP`。
   * 反解：`cards = (inner + GAP) / (cardW + GAP)` —— 注意分子分母都要 **+GAP**，
   * 因为模型里卡宽和间隙各出现 cards 次、只有两端各多出一次 GAP。
   * 早先写成 `floor((inner + GAP)/(cardW + GAP))` 却让 slot 用另一个公式，
   * 两边不自洽 ⇒ slot 恒少一格，卡片全体重叠（判据当场变红）。
   */
  const countAt = (cardW: number) =>
    cardW >= NODE_W_MIN ? Math.floor((inner + NODE_GAP) / (cardW + NODE_GAP)) : 0

  /**
   * 返回「可见卡数」。折叠卡由调用方另外 +1，所以这里必须先验
   * **连折叠卡一起放得下、且留得出 NODE_GAP 间隙**：
   * 336px 时 2 张 150 卡能「塞下」（余量 0）但间隙只有 4px，卡片贴死。
   */
  const usable = (cardW: number): { visible: number; cardW: number; foldable: boolean } | null => {
    const total = countAt(cardW)
    if (total < 1) return null
    // 「塞得下」不等于「留得出间隙」：2 张 150 卡在 320px 里余量为 -16。
    if (inner - total * cardW - (total + 1) * NODE_GAP < 0) return null
    if (!reservingFoldSlot) return { visible: total, cardW, foldable: true }
    // 要折叠 ⇒ 至少得有两张卡的位置（1 可见 + 1 折叠）。只有 1 个槽位时
    // 「折叠」根本无处安放，必须报不可折叠 —— 早先这里照常返回 total=1，
    // 调用方据此又补了一张折叠卡，320px 下直接重叠（判据当场变红）。
    if (total < 2) return { visible: total, cardW, foldable: false }
    return { visible: total - 1, cardW, foldable: true }
  }

  // 先试满宽卡，再试最窄卡；都排不下就只能单张铺满（兜底）。
  // 注意：折叠模式下若某档返回 foldable=false，要**继续试下一档**而不是就地返回 ——
  // 320px 下满宽卡只有 1 个槽位（不可折叠），最窄卡才有 2 个（可折叠）。
  let plainFallback: { visible: number; cardW: number; foldable: boolean } | null = null
  for (const cardW of [NODE_W, NODE_W_MIN]) {
    const hit = usable(cardW)
    if (hit === null) continue
    if (!reservingFoldSlot || hit.foldable) return hit
    if (plainFallback === null) plainFallback = hit // 记下「不折叠也可行」的版本
  }
  return plainFallback ?? { visible: 1, cardW: Math.max(1, Math.min(NODE_W, inner)), foldable: false }
}
/** 每层中心线的高度占比（自上而下）。网关核心那一层由大方块自己占位。 */
const ROW_RATIO: Record<TopoColumn, number> = {
  client: 0.09,
  gateway: 0.5,
  provider: 0.74,
  model: 0.93,
}

/**
 * 网关核心大方块：横向占 78%。
 *
 * 高度占比 0.40 是**算出来的**，不是拍脑袋：大方块底边必须让开供应端
 * 节点卡的**上沿**（`rowY.provider - NODE_H/2`），而不能只让开中心线。
 * 取 0.42 时底边落在 440、节点卡上沿 438 —— 压了 2px，肉眼几乎看不出，
 * 但它是真重叠，所以判据按节点卡而不是按中心线来钉。
 */
const GATEWAY_BOX = { xRatio: 0.11, yRatio: 0.29, wRatio: 0.78, hRatio: 0.4 }
/**
 * 3×3 网格。`header` 是**必须预留的标题带**：
 * 大方块顶部有一行「网关核心 · 缓存·脱敏·…」的标题（组件里是正常流的
 * `.gw__head`，不在格子里）。不预留的话第一行模块会从 `pad=10px` 处
 * 开始，正好被标题压住 —— 这是真实组件里同样会发生的重叠，
 * 所以必须在**布局函数**里让开，而不是靠 CSS 碰运气。
 */
const GATEWAY_GRID = { cols: 3, rows: 3, pad: 10, gap: 8, header: 28 }

/** 稳定字符串哈希（FNV-1a 32bit）：只用于派生抖动，不用于安全。 */
export function stableHash(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash >>> 0
}

/** 把 [min,max] 确定性抖动。 */
function jitter(id: string, salt: number, min: number, max: number): number {
  const unit = (stableHash(`${id}#${salt}`) % 1000) / 1000
  return min + unit * (max - min)
}

/** 计算网关核心大方块的几何（内部 3×3 模块格）。 */
export function computeGatewayBox(width: number, height: number): GatewayCoreBox {
  const x = Math.round(width * GATEWAY_BOX.xRatio)
  const y = Math.round(height * GATEWAY_BOX.yRatio)
  const w = Math.round(width * GATEWAY_BOX.wRatio)
  const h = Math.round(height * GATEWAY_BOX.hRatio)
  const innerW = w - GATEWAY_GRID.pad * 2
  const innerH = h - GATEWAY_GRID.pad * 2 - GATEWAY_GRID.header
  const cellW = (innerW - GATEWAY_GRID.gap * (GATEWAY_GRID.cols - 1)) / GATEWAY_GRID.cols
  const cellH = (innerH - GATEWAY_GRID.gap * (GATEWAY_GRID.rows - 1)) / GATEWAY_GRID.rows
  const gridTop = y + GATEWAY_GRID.pad + GATEWAY_GRID.header
  const cells = GATEWAY_MODULES.map((mod) => ({
    key: mod.key,
    x: Math.round(x + GATEWAY_GRID.pad + mod.col * (cellW + GATEWAY_GRID.gap)),
    y: Math.round(gridTop + mod.row * (cellH + GATEWAY_GRID.gap)),
    w: Math.round(cellW),
    h: Math.round(cellH),
  }))
  return { x, y, w, h, cells }
}

export function computeLayout(input: LayoutInput): TopoLayout {
  // 用容器真实宽度（只兜住退化输入），不再强行撑到 640 —— 见 MIN_LAYOUT_WIDTH。
  const width = Math.max(MIN_LAYOUT_WIDTH, Math.floor(input.width || 0))
  const height = Math.max(MIN_LAYOUT_HEIGHT, Math.floor(input.height || 0))
  const gateway = computeGatewayBox(width, height)

  const rowY = {} as Record<TopoColumn, number>
  for (const column of TOPO_COLUMNS) {
    rowY[column] = Math.round(height * ROW_RATIO[column])
  }

  const laid: TopoLayoutNode[] = []
  /** 被折叠节点的原始 id → 所在层的折叠节点 id（链路改指过去，免得图看起来断线）。 */
  const foldedInto = new Map<string, string>()

  for (const column of TOPO_COLUMNS) {
    if (column === 'gateway') continue // 网关核心由大方块承载，不再是单个小节点
    const inLayer = input.nodes.filter((n) => n.column === column)
    if (!inLayer.length) continue
    const ordered = [...inLayer].sort((a, b) => b.metric.requests - a.metric.requests || a.id.localeCompare(b.id))
    // 放得下就照常画；放不下先试「折叠一位」，**连折叠位都放不下就彻底不折叠**
    // —— 那样做只会把折叠节点挤出右边界（320px 容器实测：右缘 345 > 320）。
    const plain = fitRow(width, false)
    let { visible, cardW } = plain
    let needsFold = ordered.length > plain.visible
    if (needsFold) {
      const folded = fitRow(width, true)
      // `foldable=false` 表示「折叠卡会贴死/越界」⇒ 宁可只画几张，不越界。
      if (folded.foldable) {
        visible = folded.visible
        cardW = folded.cardW
      } else {
        needsFold = false
      }
    }
    const cap = Math.min(ordered.length, visible)
    const shown = ordered.slice(0, cap)
    const hidden = needsFold ? ordered.slice(cap) : []
    const rowNodes = shown.length > 0 ? shown : ordered.slice(0, 1)

    const inner = width - SIDE_PAD * 2
    // 卡片总数 = 可见卡 + (折叠节点?1:0)。
    // **间距必须与 fitRow 同一套槽位模型**：`inner = cards*cardW + (cards+1)*GAP`
    // ⇒ `gap = (inner - cards*cardW) / (cards+1)`。
    // 早先把分母写成 cards 再减一次 GAP ⇒ slot 恒少一格，卡片全体重叠
    // （1440 下最右缘只有 282 而非 1416）。
    // 折叠节点取**最后一格** —— 早先让它取 `slot * rowNodes.length`，
    // 那正是最后一张可见卡的位置，「其他 N」压在真节点上（480 下 x 都是 164）。
    const cards = rowNodes.length + (hidden.length ? 1 : 0)
    const gap = (inner - cards * cardW) / (cards + 1)
    // 步进 = 卡宽 + 间隙。上面算出的 gap 只是**间隙**，步进必须自己加上卡宽 ——
    // 早先直接拿 gap 当步长用，于是每张卡只前进「间隙」那么点，8 张卡
    // 挤在 354px 里全体重叠（1440 实测最右缘 354 而非 1416）。
    const step = cardW + gap
    rowNodes.forEach((node, index) => {
      // 抖动**先扣掉保底间隙再在余量里取**：相邻两卡抖动差最多 2×amp，
      // 要让「gap - 2×amp」仍 ≥ NODE_GAP。
      // 早先写成 `(gap - NODE_GAP)/2` 只挡住了一半（560px 实测只剩 4px），
      // 而且 gap 本身可能已小于 NODE_GAP（fitRow 只保证不放不下，不保证余量）。
      const amp = Math.max(0, Math.min(8, step / 8, (gap - NODE_GAP) / 2))
      const jitterX = rowNodes.length > 1 && amp > 0 ? jitter(node.id, 1, -amp, amp) : 0
      const jitterY = rowNodes.length > 1 ? jitter(node.id, 2, -Math.min(4, Math.max(0, gap / 2)), Math.min(4, Math.max(0, gap / 2))) : 0
      laid.push({
        ...node,
        w: cardW,
        h: NODE_H,
        x: Math.round(SIDE_PAD + gap + step * index + jitterX),
        y: Math.round(rowY[column] - NODE_H / 2 + jitterY),
      })
    })

    if (hidden.length) {
      const aggId = `${column}:folded:${ordered.length}`
      // 标签用**被折叠的请求量**，不是节点个数。
      // 早先写 `+${hidden.length}`（节点数）⇒ 同一份数据在 1440 显示「无折叠」、
      // 480 显示「+7」，用户会以为**流量在变**，而实际只是屏幕窄。
      // 请求量与宽度无关，折叠前后守恒 —— 这才是用户要的那个数。
      const hiddenRequests = hidden.reduce((s, n) => s + n.metric.requests, 0)
      const agg: TopoLayoutNode = {
        id: aggId,
        column,
        label: `+${hiddenRequests}`,
        sub: column,
        health: 'unknown',
        live: false,
        isAggregate: true,
        metric: {
          requests: 0, success: 0, failure: 0, tokens: 0, inFlight: 0,
        },
        w: cardW,
        h: NODE_H,
        x: Math.round(SIDE_PAD + gap + step * (cards - 1)),
        y: Math.round(rowY[column] - NODE_H / 2),
      }
      for (const n of hidden) {
        foldedInto.set(n.id, aggId)
        agg.metric.requests += n.metric.requests
        agg.metric.success += n.metric.success
        agg.metric.failure += n.metric.failure
        agg.metric.tokens += n.metric.tokens
        agg.metric.inFlight += n.metric.inFlight
      }
      // sub 说明「+N 是请求量、括号里是节点数」，避免让人猜单位。
      // 不用中文：硬编码中文会推高 i18n 棘轮（5757/5529），而为了这一行
      // 去开 8 语言词条不值得。用符号表达单位即可，图标层另有 tooltip。
      agg.sub = `(${hidden.length} items)`
      laid.push(agg)
    }
  }

  const byId = new Map(laid.map((n) => [n.id, n]))
  const edgeByKey = new Map<string, TopoLayoutEdge>()
  for (const edge of input.edges) {
    // 被折叠的节点：链路改指折叠节点，保持连通；两端都折叠到同一个就成自环，丢掉。
    const from = foldedInto.get(edge.from) ?? edge.from
    const to = foldedInto.get(edge.to) ?? edge.to
    if (from === to) continue
    const a = byId.get(from)
    const b = byId.get(to)
    if (!a || !b) continue
    const key = `${from}->${to}`
    const existing = edgeByKey.get(key)
    // 多条链路折叠后可能合并成同一条，取请求数之和而不是让后者盖掉前者。
    if (existing) {
      existing.requests += edge.requests
      existing.failure += edge.failure
      existing.lastStatus = edge.lastStatus ?? existing.lastStatus
      if (typeof edge.lastLatencyMs === 'number') existing.lastLatencyMs = edge.lastLatencyMs
      // 记下这条被并进来的原始 id，否则折叠后高亮找不到它
      existing.sourceIds.push(edge.id)
      continue
    }
    // 纵向流程：客户端/供应端 → 模型/网关 走下边；反之走上边。
    const downward = a.y <= b.y
    const y1 = downward ? a.y + a.h : a.y
    const y2 = downward ? b.y : b.y + b.h
    const x1 = a.x + a.w / 2
    const x2 = b.x + b.w / 2
    const midY = (y1 + y2) / 2
    const bow = jitter(key, 3, -14, 14)
    edgeByKey.set(key, {
      ...edge,
      id: key,
      from,
      to,
      // 折叠后布局边的 id 可能与原始 id 不同，高亮要能按原始 id 命中
      sourceIds: [edge.id],
      path: [
        { x: x1, y: y1 },
        { x: x1 + bow, y: midY },
        { x: x2 + bow * 0.4, y: midY },
        { x: x2, y: y2 },
      ],
    })
  }

  return { nodes: laid, edges: [...edgeByKey.values()], width, height, rowY, gateway }
}

/**
 * 高亮判定：一条布局边是否落在高亮集合里。
 *
 * 必须同时看**布局边 id** 与其**原始** id（`sourceIds`）：窄屏折叠后布局边
 * id 变成 `client:x->model:folded:8`，而视图 `edgePathFor` 产出的是原始
 * `client:x->model:deepseek-chat`。只比 id 的话，折叠一发生高亮就静默失效
 * （用户点了面板条目，图上却没有任何反应）。
 *
 * 抽成纯函数是因为 Canvas 在 jsdom 里拿不到 context，直接测组件测不到这层。
 */
export function isEdgeHighlighted(
  highlight: ReadonlySet<string>,
  edgeId: string,
  sourceIds?: readonly string[],
): boolean {
  if (highlight.has(edgeId)) return true
  return !!sourceIds?.some((s) => highlight.has(s))
}

/** 边粗细（px）：按窗口内请求数对数映射，上限 5px。
 *  0 请求 → 0.8px 的中性细线（**不是**红色：没流量≠故障）。 */
export function edgeWidth(requests: number): number {
  if (requests <= 0) return 0.8
  return Math.min(5, 0.8 + Math.log10(requests + 1) * 1.6)
}

/** 粒子大小（半径 px）：按该请求 token 量分 3 档；无 token 走中档。 */
export function particleRadius(totalTokens: number | null | undefined): number {
  if (!totalTokens || totalTokens <= 0) return 3
  if (totalTokens < 500) return 2.4
  if (totalTokens < 4000) return 3.4
  return 4.6
}
