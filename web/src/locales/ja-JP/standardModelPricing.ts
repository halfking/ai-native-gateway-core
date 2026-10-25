// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// standardModelPricing.ts — 標準モデル価格管理ページ (StandardModelPricingView) 文案。
// 名前空間:page / desc / settings / table / editModal / status / feedback。
// 技術用語は翻訳しません:canonical_name、credits_per_1m_in/out/cache_in/cache_out、cents_per_credit、global_discount、CNY/USD。
export default {
  page: {
    title: '価格管理',
    refresh: '更新',
    refreshLoading: '読み込み中…',
  },
  desc: 'テナント側の販売価格 (クレジット/1M トークン)。グローバル基準 × 割引係数が<strong>手動設定されていない</strong>ディメンションに適用されます。「手動」にチェックを入れたフィールドはグローバル変更の影響を受けません。',
  settings: {
    title: 'グローバル基準 (クレジット / 1M トークン)',
    inputToken: '入力トークン',
    outputToken: '出力トークン',
    cacheReadToken: 'キャッシュ読み取りトークン',
    cacheWriteToken: 'キャッシュ書き込みトークン',
    discount: '全体割引',
    discountHint: '手動以外のすべてのディメンションを統一割引',
    centsPerCredit: 'クレジット単価 (セント/クレジット)',
    currencyDisplay: '表示通貨',
    save: 'グローバル設定を保存',
    saving: '保存中…',
    effective: '効果 ≈ {value} (割引含む)',
  },
  filter: {
    summary: '手動 {custom}：グローバル {defaults} / {total}',
  },
  table: {
    colModel: '標準モデル',
    colStatus: 'ステータス',
    edit: '価格',
    reset: '復元',
    empty: 'モデルがありません',
    statusDefault: 'グローバル',
    statusFullCustom: 'すべて手動',
    statusPartialCustom: '{n}/4 手動',
    manualTagTitle: '手動価格',
    manualTag: '手',

    colModality: "モダリティ",

    modalityText: "テキスト",

    modalityVision: "画像",

    modalityAudio: "音声",

    modalityVideo: "動画",

    modalityMultimodal: "マルチモーダル",

    modalityEmbedding: "埋め込み",

    modalityOther: "その他",

    hasMultimodal: "マルチモーダルの手動定价を含む",
  },
  editModal: {
    title: '手動価格：{name}',
    hint: '「手動」にチェックを入れクレジットを入力。未チェックのディメンションはグローバル基準 × 割引に従います。',
    fieldSuffix: '：手動価格',
    resetBase: '基準に戻す',
    globalApprox: 'グローバル ≈ {value}',
    save: '保存',
    saving: '保存中…',
    fillGlobal: '現在のグローバルを入力',
    cancel: 'キャンセル',
    resetConfirm: '{name} のすべてのディメンションをグローバル基準に戻しますか？',

    sectionText: "テキストの Token ディメンション",

    sectionMultimodal: "マルチモーダルな Token ディメンション（画像 / 音声 / 動画）",



  },
  field: {

    image: "画像",

    audio: "音声",

    video: "動画",
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: "グローバル基準のみ",
  customOnly: "手動定价を含む",
  partialOnly: "一部のみ手動",
  otherVendor: "その他",
  loadFailed: '読み込み失敗',
  saved: "グローバル基準を保存しました（手動定价していないディメンションにのみ影響）",
  saveFailed: "保存失敗",
  needOneManual: "「手動定价」にチェックを 1 つ以上付けてください",
  resetFailed: "リセットに失敗しました",
  input: "入力",
  output: "出力",
  cacheRead: "キャッシュ読み",
  cacheWrite: "キャッシュ書き込み",

  batch: {
    selected: "{n} 件を選択中",
    paste: "選択項目に貼り付け",
    price: "一括定价",
    resetAll: "すべてグローバル基準に戻す",
    resetAllConfirm: "{n} 件のモデルの手動定价をすべてグローバル基準に戻しますか？",
    fillGlobalConfirm: "現在のグローバル基準 × 割引を {n} 件のモデルに書き込みます（手動フラグは保持）？",
    msgPasted: "{n} 件のモデルを更新しました",
    msgReset: "{n} 件のモデルをグローバル基準に戻しました",
    msgFilled: "{n} 件のモデルに書き込みました",
    msgFailed: "一括操作に失敗しました",

    fillGlobal: "現在のグローバル値を反映",
  },
}
