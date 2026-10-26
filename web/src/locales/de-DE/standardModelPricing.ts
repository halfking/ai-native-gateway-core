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
    effective: 'Wirksam ≈ {value} (inkl. Rabatt)',
  },
  filter: {
    summary: 'Manuell {custom}: Global {defaults} / {total}',
  },
  table: {
    colModel: 'Standardmodell',
    colStatus: 'Status',
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
    title: 'Manuelle Preisgestaltung: {name}',
    hint: 'Aktivieren Sie „Manuell" und tragen Sie Credits ein; nicht aktivierte Dimensionen folgen weiterhin der globalen Basis × Rabatt.',
    fieldSuffix: ': Manuelle Preisgestaltung',
    resetBase: 'Auf Basis zurücksetzen',
    globalApprox: 'Global ≈ {value}',
    save: 'Speichern',
    saving: 'Wird gespeichert…',
    fillGlobal: 'Aktuellen Globalwert übernehmen',
    cancel: 'Abbrechen',
    resetConfirm: 'Alle Dimensionen von {name} auf die globale Basis zurücksetzen?',

    sectionText: 'Text-token dimensions',

    sectionMultimodal: 'Multimodale Token-Dimensionen (Vision / Audio / Video)',



  },
  field: {

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: 'Nur globale Basis',
  customOnly: 'Mit manueller Preisgestaltung',
  partialOnly: 'Teilweise manuell',
  otherVendor: 'Andere',
  loadFailed: 'Preise der Standardmodelle konnten nicht geladen werden. Bitte erneut versuchen.',
  saved: 'Globale Basis gespeichert (betrifft nur nicht manuelle Dimensionen)',
  saveFailed: 'Preise der Standardmodelle konnten nicht gespeichert werden. Bitte erneut versuchen.',
  needOneManual: 'Bitte aktivieren Sie mindestens eine Dimension „Manuelle Preisgestaltung"',
  resetFailed: 'Preise der Standardmodelle konnten nicht zurückgesetzt werden. Bitte erneut versuchen.',
  input: 'Eingabe',
  output: 'Ausgabe',
  cacheRead: 'Cache lesen',
  cacheWrite: 'Cache schreiben',

  batch: {
    selected: '{n} ausgewählt',
    paste: 'Auf Auswahl einfügen',
    price: 'Sammelpreise',
    resetAll: 'Alle auf global zurücksetzen',
    resetAllConfirm: 'Alle manuellen Preise von {n} Modellen auf die globale Basis zurücksetzen?',
    fillGlobalConfirm: 'Aktuelle globale Basis × Rabatt auf {n} Modelle schreiben (manuelle Markierungen bleiben erhalten)?',
    msgPasted: '{n} Modelle aktualisiert',
    msgReset: '{n} Modelle zurückgesetzt',
    msgFilled: '{n} Modelle beschrieben',
    msgFailed: 'Sammelaktion konnte nicht abgeschlossen werden. Bitte erneut versuchen.',

    fillGlobal: 'Fill current global',
  },
}
