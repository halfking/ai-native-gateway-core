// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: 'The export could not start: the details or audit records could not be retrieved. Please retry the export.',
  exportNoChannel: 'The export could not start: this host has no usable export channel. Please export from the desktop app instead.',
  timelineLoadFailed: 'The data for the last 24 hours could not be loaded. Please retry.',
  detailLoadFailed: 'The details could not be loaded. Please retry.',
  auditLogLoadFailed: 'The audit log could not be loaded. Please retry.',
}
