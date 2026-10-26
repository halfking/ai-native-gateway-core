// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: '导出没能开始：详情或审计记录没能取到。请重试导出',
  exportNoChannel: '导出没能开始：当前宿主没有可用的导出通道。请改用桌面端导出',
  timelineLoadFailed: '近 24 小时的数据没能加载出来，请重试',
  detailLoadFailed: '详情没能加载出来，请重试',
  auditLogLoadFailed: '审计日志没能加载出来，请重试',
}
