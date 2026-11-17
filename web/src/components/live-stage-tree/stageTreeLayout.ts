// stageTreeLayout.ts —— 请求阶段树的**版面契约**（2026-11-17 设计对齐轮）
//
// ★ 为什么单独一份：阶段列头（sticky 表头）与每条请求带**必须共用同一套列宽**，
//   否则「表头的『路由』」与「带里的路由节点」会错位半格 ——
//   而错位的表头比没有表头更坏（它会让人把数读错列）。
//   ⇒ 列宽与列位只在这里定义一次，组件引用，不各写一份。
//
// ★ 列位与设计原型（docs/audit/2026-11-16-live阶段树原型-完整紧凑双模式版.html）
//   的对应关系：
//   · 原型的列序是 owner 初版列举序（鉴权→审批→注入→脱敏→压缩→路由→供应商）；
//     本实现的列序是**代码真实管线序**（鉴权→脱敏→审批→注入→路由→压缩→供应商），
//     依据是设计主档 §5.2「阶段顺序＝代码真实管线顺序」—— 界面跟代码走。
//   · 接收轨的**落列**按「与发送轨同身份」对齐（原型的做法）：
//     首字节↔供应商列、还原↔脱敏列、计量↔审批列、回复↔鉴权列。
//
// ★ 错误列（owner 第 9 轮：「在最右边显示错误源头及类型」）：
//   紧凑模式把错误做成**带级固定右列**（不是挂在内容流末尾）——
//   挂在末尾 ⇒ 每行位置都不同，眼睛没法竖着扫；固定成列 ⇒ 右缘恒定，一次竖扫看完。

import type { StageKey } from '../../types/liveStageTree'

/** 完整模式的网格列宽（时间 / 客户端 / 发送轨 7 列 / 接收轨标记）。 */
export const FULL_GRID_COLUMNS =
  '56px 168px 104px 104px 104px 104px 104px 104px 132px 76px'

/**
 * 紧凑模式的网格列宽（时间 / 客户端 / 11 段瓦片 / 右侧行内块 / 最右错误列）。
 *
 * ★ 几何来自设计原型（docs/audit/2026-11-16-live阶段树原型-完整紧凑双模式版.html）：
 *   · CTILE=28（瓦片边长）、CPITCH=38（瓦片步距 ⇒ 瓦片间隙 10px）
 *   · CX0=198（瓦片起点）＝ 时间列 52 + 间隙 10 + 客户端 126 + 间隙 10
 *   · CTREE_W=1400（紧凑树宽）、CERR_W=210（错误列宽）、CERR_RIGHT=10（右留白）
 *   · 行内块被约束在错误列左侧（原型 right:232px ⇒ 错误列 210 + 右留白 10 + 间距 12）
 *   ⇒ **错误列的右缘对所有带恒定**（一条真正的列，能竖着扫）。
 */
export const COMPACT_GRID_COLUMNS =
  '52px 126px repeat(11, 28px) minmax(0, 1fr) 210px'

/** 紧凑模式树宽（px）—— 错误列因此贴在**树的**右缘，而不是贴在内容末尾。 */
export const COMPACT_TREE_WIDTH = 1400

/** 紧凑模式瓦片的网格列号（时间 1 / 客户端 2 / 瓦片 3..13 / 行内块 14 / 错误列 15）。 */
export const COMPACT_TILE_COLUMN: Readonly<Partial<Record<StageKey, number>>> = {
  auth: 3,
  sanitize: 4,
  approval: 5,
  injection: 6,
  route: 7,
  compress: 8,
  provider: 9,
  first_byte: 10,
  restore: 11,
  meter: 12,
  reply: 13,
}

/** 紧凑模式右侧行内块的列号。 */
export const COMPACT_RIGHT_COLUMN = 14

/** 紧凑模式最右错误列的列号。 */
export const COMPACT_ERROR_COLUMN = 15

/** 发送轨节点的列位（网格列号，1 起）。 */
export const SEND_COLUMN: Readonly<Partial<Record<StageKey, number>>> = {
  auth: 3,
  sanitize: 4,
  approval: 5,
  injection: 6,
  route: 7,
  compress: 8,
  provider: 9,
}

/** 接收轨节点的列位（网格列号，1 起；与发送轨同身份对齐）。 */
export const RECV_COLUMN: Readonly<Partial<Record<StageKey, number>>> = {
  reply: 3,
  restore: 4,
  meter: 5,
  first_byte: 9,
}

/** 紧凑模式瓦片的列位（发送 7 段 → 回程 4 段，单行流水）。 */
export const COMPACT_COLUMN = COMPACT_TILE_COLUMN

/** 表头用的列名（key → i18n 键；★ 逐字面量在组件里取，动态键是引用扫描器的盲区）。 */
export const HEADER_COLUMN_LABEL: Readonly<Record<StageKey, string>> = {
  auth: 'liveStageTree.stage.auth',
  sanitize: 'liveStageTree.stage.sanitize',
  approval: 'liveStageTree.stage.approval',
  injection: 'liveStageTree.stage.injection',
  route: 'liveStageTree.stage.route',
  compress: 'liveStageTree.stage.compress',
  provider: 'liveStageTree.stage.provider',
  first_byte: 'liveStageTree.stage.firstByte',
  restore: 'liveStageTree.stage.restore',
  meter: 'liveStageTree.stage.meter',
  reply: 'liveStageTree.stage.reply',
}
