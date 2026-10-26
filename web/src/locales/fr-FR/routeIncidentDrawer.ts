// routeIncidentDrawer.ts — RouteIncidentDrawer 导出失败文案。
// R48-D4（R48 报告 §五-6）收口：该组件此前全硬编码中文，本轮把
// 导出链两条失败文案接进 i18n；其余存量串仍登记在 CJK 基线棘轮。
export default {
  exportFailedPrep: 'L\'export n\'a pas pu démarrer : le détail ou le journal d\'audit est introuvable. Réessayez l\'export.',
  exportNoChannel: 'L\'export n\'a pas pu démarrer : aucun canal d\'export n\'est disponible sur cet hôte. Réessayez depuis le client de bureau.',
  timelineLoadFailed: 'Le chargement des données des dernières 24 heures a échoué. Veuillez réessayer.',
  detailLoadFailed: 'Le chargement du détail a échoué. Veuillez réessayer.',
  auditLogLoadFailed: 'Le chargement du journal d’audit a échoué. Veuillez réessayer.',
}
