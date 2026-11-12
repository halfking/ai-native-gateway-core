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

    colModality: "Modalität",

    modalityText: 'Text',

    modalityVision: "Bilderkennung",

    modalityAudio: 'Audio',

    modalityVideo: 'Video',

    modalityMultimodal: 'Multimodal',

    modalityEmbedding: 'Embedding',

    modalityOther: "Sonstige",

    hasMultimodal: "Mit manuellen Preisen für mehrere Modalitäten",
  },
  editModal: {
    title: 'Manuelle Preisgestaltung: {name}',
    hint: 'Aktivieren Sie „Manuell" und tragen Sie Credits ein; nicht aktivierte Dimensionen folgen weiterhin der globalen Basis × Rabatt.',
    fieldSuffix: ': Manuelle Preisgestaltung',
    resetBase: 'Auf Basis zurücksetzen',
    globalApprox: 'Global ≈ {value}',
    save: 'Speichern',
    saving: 'Wird gespeichert…',
    fillGlobal: "Aktuelle globale Werte übernehmen",
    cancel: 'Abbrechen',
    resetConfirm: 'Alle Dimensionen von {name} auf die globale Basis zurücksetzen?',

    sectionText: "Dimensionen für Text-Token",

    sectionMultimodal: 'Multimodale Token-Dimensionen (Vision / Audio / Video)',



  },
  field: {

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },

  market: {
    open: "Marktpreise",
    title: "Marktpreisvergleich (salesdecorate-Feed)",
    syncAsOf: "Stand {date} · {rows} Zeilen übernommen",
    resolved: "{n} zu diesem Gateway aufgelöst",
    searchPlaceholder: "Modellname suchen…",
    onlyResolved: "Nur aufgelöste",
    search: "Suchen",
    empty: "Marktdaten leer - scripts/market-price-sync.py auf 211 ausführen oder auf den täglichen Crawl warten",
    colModel: "Modell",
    colMarketMin: "Marktminimum in/out (CNY/1M)",
    colFloors: "Übersee-/Inlandsuntergrenze (Originalwährung · Plattformen)",
    colMarketMinOut: "Marktminimum Output (CNY/1M)",
    colVendor: "Herstelleranker in/out (CNY/1M)",
    colRetail: "Unser Einzelpreis in/out (CNY/1M)",
    colPlatforms: "Plattformen",
    colDrift: "Einzelpreis / Markt",
    unresolved: "Unaufgelöst",
  },

  history: {
    title: "Preishistorie",
    changesTitle: "Preisänderungen",
    open: "Historie",
    openChanges: "Preisänderungen",
    empty: "Für dieses Modell wurden noch keine Preiszeiträume erfasst.",
    current: "aktuell",
    until: "bis {time}",
    mode: "Abrechnungsmodus",
    source: "Quelle",
    modePayAsYouGo: "Nach Verbrauch",
    modeTokenPlan: "Token-Tarif",
    modeBooster: "Zusatzpaket",
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

    fillGlobal: "Aktuelle globale Werte übernehmen",
  },
}
