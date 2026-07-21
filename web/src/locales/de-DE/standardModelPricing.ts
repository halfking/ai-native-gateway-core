// Auto-translated draft (de-DE) · 2026-07-02 · please review
// standardModelPricing.ts — Standardmodell-Preisseite (StandardModelPricingView) Texte.
// Namespaces: page / desc / settings / table / editModal / status / feedback.
// Technische Begriffe bleiben unübersetzt: canonical_name, credits_per_1m_in/out/cache_in/cache_out, cents_per_credit, global_discount, CNY/USD.
export default {
  page: {
    title: 'Preisverwaltung',
    refresh: 'Aktualisieren',
    refreshLoading: 'Wird geladen…',
  },
  desc: 'Mandantenseitige Verkaufspreise (Credits / 1M Token). Die globale Basis × Rabatt wird auf <strong>nicht manuelle</strong> Dimensionen angewendet; manuelle Felder sind von globalen Änderungen nicht betroffen.',
  settings: {
    title: 'Globale Basis (Credits / 1M Token)',
    inputToken: 'Eingabe-Token',
    outputToken: 'Ausgabe-Token',
    cacheReadToken: 'Cache-Lese-Token',
    cacheWriteToken: 'Cache-Schreib-Token',
    discount: 'Globaler Rabatt',
    discountHint: 'Wird einheitlich auf alle nicht manuellen Dimensionen angewendet',
    centsPerCredit: 'Credit-Einheitspreis (Cent/Credit)',
    currencyDisplay: 'Anzeigewährung',
    save: 'Globale Einstellungen speichern',
    saving: 'Wird gespeichert…',
    saved: 'Globale Basis gespeichert (betrifft nur nicht manuelle Dimensionen)',
    effective: 'Wirksam ≈ {value} (inkl. Rabatt)',
  },
  filter: {
    title: 'Preisverwaltung · Modellfilter',
    placeholder: 'Standardmodelle suchen…',
    allPricing: 'Alle Preise',
    defaultOnly: 'Nur globale Basis',
    customOnly: 'Mit manueller Preisgestaltung',
    partialOnly: 'Teilweise manuell',
    otherVendor: 'Andere',
    summary: 'Manuell {custom} · Global {defaults} / {total}',
  },
  table: {
    colModel: 'Standardmodell',
    colVendor: 'Anbieter',
    colStatus: 'Status',
    colActions: '',
    edit: 'Preis',
    reset: 'Zurücksetzen',
    empty: 'Keine Modelle',
    statusDefault: 'Global',
    statusFullCustom: 'Vollständig manuell',
    statusPartialCustom: '{n}/4 manuell',
    manualTagTitle: 'Manuelle Preisgestaltung',
    manualTag: 'M',

    colModality: 'Modality',

    modalityText: 'Text',

    modalityVision: 'Vision',

    modalityAudio: 'Audio',

    modalityVideo: 'Video',

    modalityMultimodal: 'Multimodal',

    modalityEmbedding: 'Embedding',

    modalityOther: 'Other',

    hasMultimodal: 'Has manual multimodal pricing',
  },
  editModal: {
    title: 'Manuelle Preisgestaltung · {name}',
    hint: 'Aktivieren Sie „Manuell" und tragen Sie Credits ein; nicht aktivierte Dimensionen folgen weiterhin der globalen Basis × Rabatt.',
    fieldSuffix: ' · Manuelle Preisgestaltung',
    resetBase: 'Auf Basis zurücksetzen',
    globalApprox: 'Global ≈ {value}',
    save: 'Speichern',
    saving: 'Wird gespeichert…',
    fillGlobal: 'Aktuellen Globalwert übernehmen',
    cancel: 'Abbrechen',
    needOneManual: 'Bitte aktivieren Sie mindestens eine Dimension „Manuelle Preisgestaltung"',
    resetConfirm: 'Alle Dimensionen von {name} auf die globale Basis zurücksetzen?',
    resetSuccess: 'Zurückgesetzt',
    resetFailed: 'Zurücksetzen fehlgeschlagen',

    sectionText: 'Text-token dimensions',

    sectionMultimodal: 'Multimodal-token dimensions (vision / audio / video)',

    fieldImage: 'Vision tokens',

    fieldAudio: 'Audio tokens',

    fieldVideo: 'Video tokens',
  },
  field: {
    input: 'Eingabe',
    output: 'Ausgabe',
    cacheRead: 'Cache lesen',
    cacheWrite: 'Cache schreiben',

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },
  error: {
    loadFailed: 'Laden fehlgeschlagen',
    saveFailed: 'Speichern fehlgeschlagen',
    resetFailed: 'Zurücksetzen fehlgeschlagen',
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: '仅全局基准',
  customOnly: '含手工定价',
  partialOnly: '部分手工',
  otherVendor: '其他',
  loadFailed: 'Laden fehlgeschlagen',
  saved: '全局基准已保存（仅影响未手工定价的维度）',
  saveFailed: '保存失败',
  needOneManual: '请至少勾选一个「手工定价」维度',
  resetFailed: '恢复失败',
  input: '输入',
  output: '输出',
  cacheRead: '缓存读',
  cacheWrite: '缓存写',
  refreshLoading: '加载中…',
  refresh: '刷新',
  saving: '保存中…',
  save: '保存',

  batch: {
    selected: '已选 {n} 项',
    paste: '粘贴到所选',
    price: '批量定价',
    resetAll: '全部恢复全局',
    resetAllConfirm: '将 {n} 个模型的全部手工定价恢复为全局基准？',
    fillGlobalConfirm: '把当前全局基准 × 折扣写入 {n} 个模型（保留手工标志）？',
    msgPasted: '已更新 {n} 个模型',
    msgReset: '已恢复 {n} 个模型',
    msgFilled: '已写入 {n} 个模型',
    msgFailed: '批量操作失败',

    fillGlobal: 'Fill current global',
  },
}
