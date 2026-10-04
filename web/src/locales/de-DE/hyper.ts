// hyper.ts — Hyper-Shell-Texte (Bottom-Nav / Konto-Blatt / Listenstatus / Fokus / Desktop-Hinweis).
// 2026-10-04 mit der Hyper-Laufzeit ergänzt (docs/UI规范/00 §5.4, H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'Hauptnavigation',
    home: 'Start',
    dashboard: 'Übersicht',
    requests: 'Anfragen',
    models: 'Modelle',
    more: 'Mehr',
  },
  account: {
    title: 'Konto',
    profile: 'Profil',
    language: 'Sprache',
    theme: 'Design',
    help: 'Hilfe',
    health: 'Dienststatus',
    healthy: 'Erreichbar',
    unhealthy: 'Nicht erreichbar',
    unknown: 'Status unbekannt',
    logout: 'Abmelden',
    adminEntry: 'Administration',
    close: 'Schließen',
  },
  dataView: {
    switchTo: 'Zu {mode} wechseln',
    table: 'Tabelle',
    cards: 'Karten',
  },
  list: {
    refreshing: 'Wird aktualisiert…',
    loadMore: 'Mehr laden',
    loadingMore: 'Wird geladen…',
    retry: 'Erneut versuchen',
    allLoaded: 'Alle {count} geladen',
    loadFailed: 'Laden fehlgeschlagen. Zum Wiederholen tippen.',
    empty: 'Keine Einträge',
  },
  focus: {
    enter: 'Fokusansicht',
    exit: 'Fokus beenden',
  },
  desktopOnly: {
    banner: 'Diese Seite ist für Desktop ausgelegt. Hier können Sie lesen; umfangreiche Bearbeitungen gehören auf den Desktop.',
  },
}
