// board.ts — 运营面板「更多」入口文案（board.operational.more）。
// 2026-11-21 审计轮：从 dashboard.ts 的具名导出 boardOperational 收编为独立
// 模块——具名导出的 index 重组扫描器不可见（假红根因之一），且其余 7 语种
// 此前根本没有该键 ⇒ 非中文界面裸键上屏。
export default {
  operational: {
    more: '更多',
  },
}
