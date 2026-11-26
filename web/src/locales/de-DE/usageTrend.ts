// usageTrend.ts — 全页用量趋势分析（2026-10-02 看板轮；8 语言同步）。
export default {
  pageTitle: 'Nutzungstrend-Analyse',
  pageSub: 'Nutzung je Modell über die Zeit; Filter nach Anbieter / Mandant / API-Key / Modell / Metrik',
  loadFailed: 'Laden fehlgeschlagen',
  degraded: "Trenddaten nicht verfügbar (View {view} ist nicht initialisiert). Die flache Linie bedeutet „nicht berechnet“, nicht null Nutzung.",
  detailTimeoutHint: 'Beim Filtern nach API-Key werden Anfrageprotokolle gescannt, was bei großen Zeiträumen zu einem Timeout führen kann; bitte einen kürzeren Zeitraum (z. B. innerhalb von 7 Tagen) wählen',
  filterProvider: 'Anbieter',
  filterTenant: 'Mandant',
  filterApiKey: 'API-Key',
  filterModel: 'Modell',
  filterMetric: 'Metrik',
  filterAll: 'Alle',
  filterProviderKeyFailed: "Anbieter-/Key-Filteroptionen konnten nicht geladen werden – eine leere Auswahl bedeutet nicht, dass es keine gibt",
  filterTenantFailed: 'Mandanten-Filteroptionen konnten nicht geladen werden',
  totalRow: 'Gesamt',
  shareByMetric: 'Anteil (Metrik)',
  sourceDetail: 'Anforderungsdetail',
  // 2026-10-02 Mehrfachauswahl / Alle löschen / Auto-Aktualisierung
  clearAll: 'Alle löschen',
  // 2026-11-23 深链钉住轮：被深链钉住的维度（徽标 + 解除按钮）
  pinnedRelease: 'Lösen',
  pinnedUnresolved: 'Anbieter nicht gefunden – Filter nicht angewendet',
  autoRefresh: 'Auto-Aktualisierung',
  autoRefreshOff: 'Aus',
  autoRefresh30s: '30 Sek.',
  autoRefresh1m: '1 Min.',
  autoRefresh5m: '5 Min.',
}