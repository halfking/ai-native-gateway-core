// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: 'Export konnte nicht starten: Details oder Audit-Log konnten nicht geladen werden. Bitte den Export erneut versuchen.',
  exportNoChannel: 'Export konnte nicht starten: Für diesen Host ist kein Exportkanal verfügbar. Bitte den Export im Desktop-Client starten.',
  timelineLoadFailed: 'Daten der letzten 24 Stunden konnten nicht geladen werden. Bitte erneut versuchen.',
  detailLoadFailed: 'Details konnten nicht geladen werden. Bitte erneut versuchen.',
  auditLogLoadFailed: 'Audit-Protokoll konnte nicht geladen werden. Bitte erneut versuchen.',
}
