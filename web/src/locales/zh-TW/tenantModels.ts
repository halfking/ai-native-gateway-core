// tenantModels.ts — /tenant/models 頁面文案（租戶視角：標準模型目錄 + 價格）。
export default {
  page: {
    title: '標準模型',
    desc: '我們為您的租戶開放的標準模型清單，依模型原廠分組。計費含輸入 / 輸出 / 快取讀 / 快取寫，皆按每百萬 Token 扣積分。',
    filterTitle: '標準模型 · 篩選',
    filterPlaceholder: '選擇標準模型…',
    modelCount: '{n} 個模型',
    refresh: '重新整理',
    loading: '載入中…',
    empty: '暫無可用模型',
    loadFailed: '載入失敗',
    vendorSectionTitle: '模型清單',
  },
  columns: {
    model: '標準模型',
    canonicalName: '模型 ID',
    family: '分類',
    contextWindow: '上下文',
    multimodal: '多模態',
    modalityTag: '類型',
    billingMode: '計費模式',
    inPrice: '輸入/1M',
    outPrice: '輸出/1M',
    cacheIn: '快取讀/1M',
    cacheOut: '快取寫/1M',
  },
  modalities: {
    text: '文字',
    vision: '視覺',
    audio: '音訊',
    multimodal: '多模態',
    embedding: '向量',
  },
  billing: {
    token: '按 Token 積分',
  },
  multimodal: {
    yes: '支援',
    no: '僅文字',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: '已選廠商：{vendor}',
    count: '已顯示 {n} / 共 {m}',
  },
  cta: {
    buyCredits: '購買積分',
  },
}