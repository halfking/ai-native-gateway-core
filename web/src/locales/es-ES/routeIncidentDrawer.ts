// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: 'No se pudo iniciar la exportación: no se obtuvieron el detalle ni el registro de auditoría. Vuelva a intentarlo.',
  exportNoChannel: 'No se pudo iniciar la exportación: este host no tiene canal de exportación. Reintente desde la aplicación de escritorio.',
  timelineLoadFailed: 'No se pudieron cargar los datos de las últimas 24 horas. Vuelva a intentarlo.',
  detailLoadFailed: 'No se pudo cargar el detalle. Vuelva a intentarlo.',
  auditLogLoadFailed: 'No se pudo cargar el registro de auditoría. Vuelva a intentarlo.',
}
