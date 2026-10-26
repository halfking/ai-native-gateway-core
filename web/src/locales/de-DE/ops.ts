export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'Daten konnten nicht geladen werden. Bitte erneut versuchen.',
    fillRequired: 'Bitte füllen Sie die erforderlichen Felder aus',
    projects: 'Projekte',
    sessions: 'Sitzungen',
    codeReviews: 'Code-Reviews',
    createProject: 'Projekt erstellen',
    createProjectTitle: 'Neues Projekt erstellen',
    createProjectSuccess: 'Projekt erfolgreich erstellt',
    createProjectFailed: 'Projekt konnte nicht erstellt werden. Bitte erneut versuchen.',
    newSession: 'Neue Sitzung',
    createSessionTitle: 'Neue Sitzung erstellen',
    createSessionSuccess: 'Sitzung erfolgreich erstellt',
    createSessionFailed: 'Sitzung konnte nicht erstellt werden. Bitte erneut versuchen.',
    viewSessions: 'Sitzungen anzeigen',
    viewReviews: 'Reviews anzeigen',
    showAll: 'Alle anzeigen',
    reviewDetail: 'Review-Details',
    projectName: 'Projektname',
    projectNamePlaceholder: 'Projektnamen eingeben',
    language: 'Sprache',
    languagePlaceholder: 'z. B. TypeScript',
    framework: 'Frontend-Framework',
    frameworkPlaceholder: 'z. B. Vue 3',
    projectId: 'Projekt-ID',
    sessionName: 'Sitzungsname',
    taskType: 'Aufgabentyp',
    taskTypePlaceholder: 'z. B. Code-Review, Refactoring, Erklärung',
    startedAt: 'Begonnen am',
    endedAt: 'Beendet am',
    filePath: 'Dateipfad',
    score: 'Bewertung',
    summary: 'Zusammenfassung',
    issues: 'Probleme',
    suggestions: 'Vorschläge',
    category: 'Kategorie',
    reviewedAt: 'Geprüft am',
    line: 'Zeile',
    severity: 'Schweregrad',
    message: 'Nachricht',
    /**
     * Leerzustände (2026-10-27, Inkrement 14 · „Leerzustand 5 Punkte“).
     * ★ Die drei Tabellen teilten sich früher einen Satz `hyper.list.empty`
     *   („in dieser Liste gibt es vorübergehend noch keine Einträge“), es sind aber
     *   **zwei** verschiedene Zustände:
     *   · Projekte — die rohe Liste ⇒ leer = noch nie ein Projekt angelegt (FirstUse)
     *   · Sitzungen / Reviews — die **gefilterte** Liste ⇒ leer = herausgefiltert (Filtered)
     *   ⇒ Ein Satz für beide Fälle macht aus „Sie haben noch nicht angefangen“ und
     *   „Ihr Filter ist zu eng“ dasselbe.
     */
    emptyFirstUseProjects: 'Es wurde noch kein Projekt angelegt.',
    emptyFilteredSessions: 'Mit den aktuellen Filterkriterien wurden keine Sitzungen gefunden.',
    emptyFilteredReviews: 'Mit den aktuellen Filterkriterien wurden keine Code-Reviews gefunden.',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: 'Nur Super-Administratoren können Knoten pflegen; derzeit wird nur lesend angezeigt.',
    nodeDetailDefaultTenantOnlyHint: 'Nur der default-Tenant kann Knoten pflegen; derzeit wird nur lesend angezeigt.',

















    pingFailedWithReason: 'Sitzungs-Ping fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    saveSettingFailedWithReason: 'Einstellung konnte nicht gespeichert werden: {reason}. Bitte erneut versuchen.',
    maintainFailed: 'Wartung konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
    modelMaintainFailed: 'Modellwartung konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
    fpSlotLoadFailed: 'Fingerabdruck-Slots konnten nicht geladen werden. Bitte erneut versuchen.',
    fpSlotProviderMissing: 'provider_id fehlt, Fingerabdruck-Slots konnten nicht geladen werden. Bitte zuerst einen Anbieter auswählen und erneut versuchen',
    accessErrorsHint: 'Fehlgeschlagene Aufrufe + Routing-Fehler · Originalanfrage öffnen',
    recentRequestsHint: 'Letzte 1 Stunde{source} · Anfragedetails ansehen',
  }
,

}
