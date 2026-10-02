// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: 'Aktualisieren',
    refreshing: 'Aktualisieren…',
    loadFailed: 'Laden fehlgeschlagen',
    noData: 'Keine Profildaten',
  registry: {
    version: 'Registry-Version',
    types: 'Aufgabentypen',
    correctedTotal: 'Korrekturen im Zeitraum',
    pendingSuggestions: 'Offene Tier-Änderungen',
  },
  table: {
    taskType: 'Aufgabentyp',
    description: 'Beschreibung',
    tier: 'Tier',
    fallbacks: 'Fallback-Kette',
    minConf: 'Min. Konfidenz',
    total: 'Korrekturen',
    rate: 'Korrekturquote',
    suggestion: 'Vorgeschlagenes Tier',
  },
  action: {
    apply: 'Vorschläge anwenden',
    applyConfirm: 'Korrekturgesteuerte Tier-Vorschläge in die Tier-Konfiguration (task_type_tier_config) schreiben?',
    applyDone: 'Angewendet: {types}',
    applyNone: 'Derzeit erfüllt kein Vorschlag den Schwellenwert',
    reload: 'Overlay neu laden',
    reloadConfirm: 'Die TASKPROFILE_OVERLAY-Profildatei neu laden? Ohne Konfiguration wird auf eingebettete Defaults zurückgesetzt.',
    reloadDone: 'Registry neu geladen: {version}',
    exportCsv: 'Korrekturen als CSV exportieren',
    days: 'Zeitraum (Tage)',
  },
  status: {
    applying: 'Anwenden…',
    reloading: 'Neu laden…',
    exporting: 'Exportieren…',
  },
}
