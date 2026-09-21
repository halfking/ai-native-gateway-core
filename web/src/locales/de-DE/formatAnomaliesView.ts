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

  // 2026-09-21: 请求错误 tab（reqprobe）
  tabs: {
      format: 'Antwortformat',
      request: 'Anfragefehler',
  },
  requestTab: {
      stats: {
          unresolved: 'Ungelöst (Seite)',
          autoRecovered: 'Automatisch behoben',
          total: 'Gesamt',
      },
      filter: {
          day: 'Datum',
          trigger: 'Typ',
          triggerPlaceholder: 'Typ wählen…',
      },
      trigger: {
          all: 'Alle Typen',
          param_rejected: 'Parameter abgelehnt',
          mode_mismatch: 'Modus-Fehlanpassung',
          upstream_error: 'Upstream-Fehler',
      },
      triggerDesc: {
          param_rejected: 'Upstream hat einen Anfrageparameter abgelehnt (z.B. reasoning_effort); das Gateway hat ihn entfernt und erneut versucht',
          mode_mismatch: 'API-Form (responses / chat) passt nicht zum Anbieter',
          upstream_error: 'Nicht klassifizierbarer anfrageseitiger 4xx, zu triagieren',
      },
      batch: {
          selected: '{n} ausgewählt',
          resolveSelected: 'Auswahl lösen',
          resolveFiltered: 'Alle ungelösten lösen',
      },
      table: {
          day: 'Datum',
          trigger: 'Typ',
          param: 'Parameter / Vorschlag',
          status: 'Status',
          occurrences: 'Anzahl',
          recoveredCount: '{n}× selbstheilend',
      },
      detail: {
          title: 'Anfragefehler-Detail',
          clientModel: 'Client-Modell',
          protocol: 'Ausgehendes Protokoll',
          suggestMode: 'Vorgeschlagener Modus',
          firstSeen: 'Erstmals',
          lastSeen: 'Zuletzt',
          errorSample: 'Upstream-Fehlerbeispiel',
          occurrences: 'Vorkommen',
          recoveredHint: '{n} davon automatisch per Parameterentfernung / Moduswechsel behoben',
      },
  },
}
