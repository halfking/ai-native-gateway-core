// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: '匯出沒能開始：詳情或稽核記錄沒能取到。請重試匯出',
  exportNoChannel: '匯出沒能開始：目前主機沒有可用的匯出通道。請改用桌面端匯出',
  timelineLoadFailed: '近 24 小時的資料沒能載入出來，請重試',
  detailLoadFailed: '詳情沒能載入出來，請重試',
  auditLogLoadFailed: '稽核記錄沒能載入出來，請重試',
}
