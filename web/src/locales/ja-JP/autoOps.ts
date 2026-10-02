// autoOps.ts — AUTO 路由运营统一工作台文案（2026-10-02 整合轮）。
export default {
  title: 'AUTOルート運用',
  desc: 'AUTOルーティングのループ：アノテーション → 統計 → 階層提案 → チューニング承認。反映は常にゲート + 人的承認を経る',
  tab: {
    annotate: 'アノテーション作業台',
    stats: 'アノテーション統計',
    profiles: 'タスクプロファイル',
    tuning: 'チューニング承認',
  },
  kpi: {
    pendingToday: '本日の未アノテーション',
    totalAnnotations: '累計アノテーション',
    accuracy: '分類精度',
    annotators: 'アノテーター数',
    pendingProposals: '審査待ち提案',
    suggestions: '階層提案タイプ',
    updatedAt: '{time} に更新',
    refresh: '概要を更新',
    refreshing: '更新中…',
  },
}
