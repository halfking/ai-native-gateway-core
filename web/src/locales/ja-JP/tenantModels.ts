// tenantModels.ts — /tenant/models ページの文言（テナント視点：標準モデル一覧 + 料金）。
export default {
  page: {
    title: '標準モデル',
    desc: 'テナントに開放されている標準モデルの一覧です（ベンダー別）。入力 / 出力 / キャッシュ読み / キャッシュ書き込みのいずれも、100万トークンあたりのクレジットで課金されます。',
    filterTitle: '標準モデル · 絞り込み',
    filterPlaceholder: '標準モデルを選択…',
    modelCount: '{n} モデル',
    refresh: '更新',
    loading: '読み込み中…',
    empty: '利用可能なモデルがありません',
    loadFailed: '読み込みに失敗しました',
    vendorSectionTitle: 'モデル一覧',
  },
  columns: {
    model: '標準モデル',
    canonicalName: 'モデル ID',
    family: 'ファミリー',
    contextWindow: 'コンテキスト',
    multimodal: 'マルチモーダル',
    modalityTag: '種類',
    billingMode: '課金方式',
    inPrice: '入力 / 1M',
    outPrice: '出力 / 1M',
    cacheIn: 'キャッシュ読 / 1M',
    cacheOut: 'キャッシュ書 / 1M',
  },
  modalities: {
    text: 'テキスト',
    vision: 'ビジョン',
    audio: 'オーディオ',
    multimodal: 'マルチモーダル',
    embedding: '埋め込み',
  },
  billing: {
    token: 'トークンごとのクレジット',
  },
  multimodal: {
    yes: '対応',
    no: 'テキストのみ',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: '選択中のベンダー：{vendor}',
    count: '{n} / {m} 件表示中',
  },
  cta: {
    buyCredits: 'クレジットを購入',
  },
}