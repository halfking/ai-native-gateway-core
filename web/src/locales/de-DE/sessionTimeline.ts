// sessionTimeline.ts — SessionTurnsTimeline.vue Texte (OBS-FE5 Turn-Zeitachse)
// Abdeckung: Latenz-Null-Fallback / Aktualisieren / Fehler / Leer / Mehr laden / Alle geladen / Netzwerkfehler
export default {
  latencyUnknown: 'Unbekannt',
  refresh: 'Aktualisieren',
  refreshing: 'Wird aktualisiert…',
  retry: 'Wiederholen',
  empty: 'Keine Turns in dieser Sitzung',
  loading: 'Wird geladen…',
  loadMore: 'Mehr Turns laden',
  bodyUnavailable: 'Für {n} der geladenen Turns wurde kein Anfrage-/Antworttext erfasst; die Textspeicherung ist für diese Sitzung möglicherweise deaktiviert.',
  allLoaded: '{n} Turns geladen, alle vollständig',
  errors: {
    network: 'Netzwerkfehler, bitte Verbindung prüfen und erneut versuchen',
  },
}
