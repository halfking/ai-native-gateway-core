// tenantModels.ts — Texte für die Seite /tenant/models (Mandantenansicht: Standardmodelle + Preise).
export default {
  page: {
    title: 'Standardmodelle',
    desc: 'Standardmodelle, die wir für Ihren Mandanten freigeschaltet haben. Die Abrechnung umfasst Eingabe / Ausgabe / Cache-Lesen / Cache-Schreiben, jeweils in Credits pro Million Token.',
    filterTitle: 'Standardmodelle · Filter',
    filterPlaceholder: 'Standardmodell auswählen…',
    modelCount: '{n} Modelle',
    refresh: 'Aktualisieren',
    loading: 'Wird geladen…',
    empty: 'Keine Modelle verfügbar',
    loadFailed: 'Laden fehlgeschlagen',
    vendorSectionTitle: 'Verfügbare Modelle',
  },
  columns: {
    model: 'Standardmodell',
    canonicalName: 'Modell-ID',
    family: 'Familie',
    contextWindow: 'Kontext',
    multimodal: 'Multimodal',
    modalityTag: 'Typ',
    billingMode: 'Abrechnung',
    inPrice: 'Eingabe / 1M',
    outPrice: 'Ausgabe / 1M',
    cacheIn: 'Cache-Lesen / 1M',
    cacheOut: 'Cache-Schreiben / 1M',
  },
  modalities: {
    text: 'Text',
    vision: 'Vision',
    audio: 'Audio',
    multimodal: 'Multimodal',
    embedding: 'Embedding',
  },
  billing: {
    token: 'Credits pro Token',
  },
  multimodal: {
    yes: 'Ja',
    no: 'Nur Text',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: 'Ausgewählter Anbieter: {vendor}',
    count: '{n} / {m} angezeigt',
  },
  cta: {
    buyCredits: 'Credits kaufen',
  },
}