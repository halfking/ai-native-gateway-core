// hyper.ts — Hyper シェル文言（ボトムナビ / アカウントシート / リスト状態 / フォーカス / デスクトップ専用）。
// 2026-10-04 Hyper ランタイムと併せて追加（docs/UI规范/00 §5.4, H2/H3）。
export default {
  bottomNav: {
    ariaLabel: 'メインナビゲーション',
    home: 'ホーム',
    dashboard: 'ダッシュボード',
    requests: 'リクエスト',
    models: 'モデル',
    more: 'その他',
  },
  account: {
    title: 'アカウント',
    profile: 'プロフィール',
    language: '言語',
    theme: 'テーマ',
    help: 'ヘルプ',
    health: 'サービス状態',
    healthy: '到達可能',
    unhealthy: '到達不可',
    unknown: '状態不明',
    logout: 'ログアウト',
    adminEntry: '管理コンソール',
    close: '閉じる',
  },
  dataView: {
    switchTo: '{mode} に切り替える',
    table: 'テーブル',
    cards: 'カード',
  },
  list: {
    refreshing: '更新中…',
    loadMore: 'さらに読み込む',
    loadingMore: '読み込み中…',
    retry: '再試行',
    allLoaded: '{count} 件すべて読み込み済み',
    loadFailed: '読み込みに失敗しました。タップで再試行。',
    empty: 'レコードがありません',
  },
  focus: {
    enter: 'フォーカス表示',
    exit: 'フォーカスを終了',
  },
  desktopOnly: {
    banner: 'このページはデスクトップ向けに設計されています。閲覧はできますが、大量の編集はデスクトップで行ってください。',
  },
}
