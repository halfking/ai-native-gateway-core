// usageTrend.ts — 全页用量趋势分析（2026-10-02 看板轮；8 语言同步）。
export default {
  pageTitle: '使用量トレンド分析',
  pageSub: 'モデル別の使用量推移。プロバイダー / テナント / APIキー / モデル / 指標で絞り込み',
  loadFailed: '読み込みに失敗しました',
  detailTimeoutHint: 'APIキーで絞り込む場合はリクエスト明細をスキャンするため、広い期間ではタイムアウトすることがあります。期間を短く（例：7日以内）して再試行してください',
  filterProvider: 'プロバイダー',
  filterTenant: 'テナント',
  filterApiKey: 'APIキー',
  filterModel: 'モデル',
  filterMetric: '指標',
  filterAll: 'すべて',
  shareByMetric: '指標シェア',
  sourceDetail: 'リクエスト明細',
  // 2026-10-02 複数選択/全消去/自動更新
  clearAll: 'すべてクリア',
  autoRefresh: '自動更新',
  autoRefreshOff: 'オフ',
  autoRefresh30s: '30秒',
  autoRefresh1m: '1分',
  autoRefresh5m: '5分',
}