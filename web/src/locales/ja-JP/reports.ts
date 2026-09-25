// reports.ts — レポート照合ページ（プロバイダー/内部のデュアルビュー、R65 i18n 追加）。
export default {
  // ビュー切替
  providerView: 'プロバイダー照合',
  internalView: '内部照合',
  // ツールバーアクション
  exportExcel: 'Excel エクスポート',
  rerun: '最終日を再実行',
  rerunDone: '再実行が完了しました',
  rerunFailed: '再実行に失敗しました',
  // カバー範囲 / 空状態
  daysCovered: '日分のスナップショット',
  noSnapshots: 'この期間のレポートスナップショットがありません（日次集計ジョブは早朝に前日分を生成します。「最終日を再実行」で再計算もできます）',
  // サマリーカード
  requests: 'リクエスト数',
  totalTokens: '合計トークン',
  in: '入力',
  out: '出力',
  cacheRead: 'キャッシュ読取',
  cacheWrite: 'キャッシュ書込',
  providerCost: 'プロバイダーコスト',
  cacheHit: 'キャッシュヒット',
  internalCredits: '内部クレジット',
  internalCost: '内部金額',
  // グループテーブル見出し
  byProvider: 'プロバイダー別',
  byTenant: 'テナント別',
  byPerson: '担当者別',
  byModel: 'モデル別',
  byDay: '日別',
  // 列名
  provider: 'プロバイダー',
  tenant: 'テナント',
  person: '担当者',
  model: 'モデル',
  date: '日付',
  success: '成功',
  errors: '失敗',
  cost: 'コスト',
  errorBreakdown: 'エラー内訳',
}
