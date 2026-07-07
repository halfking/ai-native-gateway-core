// formatAnomaliesView.ts — Formatanomalie-Überwachungsseite (de-DE).
export default {
  pageTitle: 'Formatanomalie-Überwachung',
  pageSubtitle: 'Schnelle Übersicht über Änderungen des Provider-Antwortformats, Token-Extraktionsfehler und Kompatibilitätsprobleme.',

  stats: {
    total: 'Gesamtanzahl Anomalien',
    unresolved: 'Ungelöst',
    critical: 'Kritisch',
    window: 'Statistikfenster',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: 'Provider auswählen…',
    model: 'Modell',
    modelPlaceholder: 'Modell auswählen…',
    anomalyType: 'Anomalietyyp',
    anomalyTypePlaceholder: 'Anomalietyyp auswählen…',
    unresolvedOnly: 'Nur ungelöste',
    query: 'Abfrage',
    refresh: 'Aktualisieren',
  },

  anomalyType: {
    all: 'Alle Anomalietypen',
    missing_usage_block: 'Fehlender Usage-Block',
    zero_completion_tokens: 'Completion Tokens gleich 0',
    extraction_failed: 'Extraktion fehlgeschlagen',
    unexpected_structure: 'Unerwartete Struktur',
    null_usage_values: 'Usage-Werte sind Null',
    token_mismatch: 'Token stimmen nicht überein',
    missing_provider_tokens: 'Fehlende Provider-Tokens',
    missing_client_tokens: 'Fehlende Client-Tokens',
    json_parse_error: 'JSON-Analysefehler',
    missing_finish_reason: 'Fehlender Finish Reason',
    missing_content: 'Fehlender Content',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'Upstream-Antwort fehlt Usage-Block',
    zero_completion_tokens: 'Antwort hat Inhalt, aber completion_tokens ist 0',
    extraction_failed: 'Keine verwendbaren Usage-Infos aus der Antwort extrahierbar',
    unexpected_structure: 'Upstream lieferte unerwartete Struktur',
    null_usage_values: 'Usage-Felder vorhanden, aber Werte sind null',
  },

  severity: {
    critical: 'Kritisch',
    high: 'Hoch',
    medium: 'Mittel',
    low: 'Niedrig',
  },

  status: {
    resolved: 'Gelöst',
    unresolved: 'Ungelöst',
  },

  table: {
    detectedAt: 'Erkannt am',
    severity: 'Schweregrad',
    anomalyType: 'Anomalietyp',
    providerModel: 'Provider / Modell',
    requestId: 'Request ID',
    tokenInfo: 'Token-Info',
    status: 'Status',
    actions: 'Aktionen',
    loading: 'Wird geladen...',
    noData: 'Keine Anomalie-Datensätze gefunden',
    viewDetail: 'Details',
    expectedTokens: 'Erwartet: {count}',
    actualTokens: 'Tatsächlich: {count}',
  },

  token: {
    expected: 'Erwartet',
    actual: 'Tatsächlich',
  },

  pager: {
    prev: 'Vorherige',
    next: 'Nächste',
    summary: 'Seite {page} / {totalPages}, {total} Datensätze',
  },

  detail: {
    title: 'Anomalie-Details',
    close: 'Schließen',
    requestId: 'Request ID',
    detectedAt: 'Erkannt am',
    provider: 'Provider',
    model: 'Modell',
    outboundModel: 'Ausgehendes Modell',
    usageSource: 'Usage Source',
    responseStructure: 'Antwortstruktur',
    responseSample: 'Antwortbeispiel',
    resolutionNotes: 'Lösungshinweise',
    resolutionNotesPlaceholder: 'Notizen zur Behebung für spätere Nachverfolgung',
    markResolved: 'Als gelöst markieren',
    processing: 'Wird verarbeitet...',
    resolutionInfo: 'Lösungsinformation',
    noNotes: 'Keine Lösungshinweise',
  },

  error: {
    loadFailed: 'Laden fehlgeschlagen',
    summaryLoadFailed: 'Statistik konnte nicht geladen werden',
    markFailed: 'Markierung fehlgeschlagen',
    needSuperAdmin: 'Super-Admin-Berechtigung erforderlich',
  },
}
