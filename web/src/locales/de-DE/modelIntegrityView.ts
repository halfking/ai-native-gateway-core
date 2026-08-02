// modelIntegrityView.ts — Modellintegritäts-Überwachungsseite (de-DE).
export default {
  pageTitle: 'Modellintegritäts-Überwachung',
  pageSubtitle: 'Verfolgen Sie Modellsubstitution, Antwortabschneiden, leere Antworten, wiederholte Inhalte und Fingerprint-Drift.',

  tabs: {
    events: 'Integritätsereignisse',
    drift: 'Fingerprint-Drift',
  },

  stats: {
    total: 'Ereignisse gesamt',
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
    severity: 'Schweregrad',
    unresolvedOnly: 'Nur ungelöste',
    query: 'Abfrage',
    refresh: 'Aktualisieren',
  },

  anomalyType: {
    all: 'Alle Anomalietypen',
    model_mismatch: 'Modell stimmt nicht überein',
    finish_refusal: 'Verweigerung / Inhaltsfilter',
    finish_truncation: 'Abschluss abgeschnitten',
    token_arith_fail: 'Token-Arithmetik fehlgeschlagen',
    empty_response: 'Leere Antwort',
    repeated_content: 'Wiederholter Inhalt',
    fingerprint_drift: 'Fingerprint-Drift',
  },

  anomalyTypeDescription: {
    model_mismatch: 'Upstream lieferte ein Modell, das nicht dem angeforderten entspricht (mögliche stille Substitution)',
    finish_refusal: 'Upstream lieferte Verweigerung oder content_filter',
    finish_truncation: 'Durch Länge / max_tokens abgeschnitten',
    token_arith_fail: 'prompt + completion entspricht nicht total',
    empty_response: 'Stream erzeugte keinen Inhalt und keine Tokens',
    repeated_content: 'Antworttext enthält große wiederholte Blöcke (mögliche Modellschleife)',
    fingerprint_drift: 'system_fingerprint wich von der Baseline ab',
  },

  severity: {
    all: 'Alle Schweregrade',
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
    actual: 'Tatsächlich',
    status: 'Status',
    actions: 'Aktionen',
    loading: 'Wird geladen...',
    noData: 'Keine Integritätsereignisse gefunden',
    viewDetail: 'Details',
  },

  drift: {
    days: 'Tage',
    query: 'Abfrage',
    noData: 'Im gewählten Fenster wurde kein Fingerprint-Drift erkannt',
  },

  pager: {
    prev: 'Vorherige',
    next: 'Nächste',
    summary: 'Seite {page} / {totalPages}, {total} Datensätze',
  },

  detail: {
    title: 'Details zum Integritätsereignis',
    close: 'Schließen',
    requestId: 'Request ID',
    detectedAt: 'Erkannt am',
    provider: 'Provider',
    model: 'Modell',
    outboundModel: 'Ausgehendes Modell',
    credential: 'Credential-ID',
    expected: 'Erwartet',
    actual: 'Tatsächlich',
    context: 'Kontext',
    sample: 'Beispiel',
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
    driftLoadFailed: 'Fingerprint-Drift konnte nicht geladen werden',
    markFailed: 'Markierung fehlgeschlagen',
    needSuperAdmin: 'Super-Admin-Berechtigung erforderlich',
  },
}
