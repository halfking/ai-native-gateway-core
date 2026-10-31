// Auto-translated draft (de-DE) · 2026-07-02 · please review
// credentialMonitor.ts — Anmeldedaten-Überwachungsseite (CredentialMonitorView).
// Spiegelt die Struktur von zh-CN/credentialMonitor.ts wider. Backend-Enum-Werte
// (ready/degraded/healthy/warning/broken/usw.) bleiben in der UI unverändert —
// eine Übersetzung würde den API-Vertrag brechen.
export default {
  page: {
    title: 'Anmeldedaten-Überwachung',
  },
  table: {
    cell: {
      modelsNotMeasured: 'Diese Anmeldedaten lieferten diesmal keine Modellzahlen (nicht gemessen) – nicht „0 verfügbar“',
    },
  },
  drawer: {
    tab: {
      overview: 'Übersicht',
      models: 'Modelle',
      requests: 'Anfragen',
    },
    operationFailedWithReason: 'Vorgang fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    offlineFailedWithReason: 'Offline-Schaltung fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    onlineFailedWithReason: 'Online-Schaltung fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    degradeFailedWithReason: 'Herabstufung fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    upgradeFailedWithReason: 'Upgrade fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    settingFailedWithReason: 'Einstellung fehlgeschlagen: {reason}. Bitte erneut versuchen.',
    manualDisabledTooltip: 'Manuell deaktiviert, die Auto-Probe greift hier nicht zu. Kann manuell gestartet werden',
    availableTooltip: 'Derzeit verfügbar, kann manuell deaktiviert werden',
    autoControlledTooltip: 'Von der Auto-Probe gesteuert (broken_confirmed usw.). Kann manuell deaktiviert werden (erzwungene Offline-Schaltung)',
    rowSelectsMonitor: 'Zeile auswählen, um die Überwachung rechts anzuzeigen',
    selectModelRow: 'Modellzeile links auswählen, um Details anzuzeigen',
    sourceSessionTurns: 'Sitzungsprotokoll',
  },
  chart: {
    allHealthy: 'Alle gesund',
    errorsWhenHealthy: 'Fehlerverteilung: alle gesund',
    errorsTitle: 'Fehlerverteilung',
    /**
     * Leerzustand der Anmeldedaten-Heatmap (2026-10-27, Inkrement 14).
     * ★ kind = **Filtered**: diese Seite hat Zeitraum + Filter (Modell, nur
     *   auffällige, Selbstprüfung ausgeschlossen) ⇒ leer heißt „herausgefiltert“,
     *   nicht „auf dieser Seite gibt es nichts“.
     */
    emptyFiltered: 'Im aktuellen Zeitraum und mit den aktuellen Filterkriterien wurden keine Anmeldedaten erfasst. Erweitern Sie Zeitraum oder Filter, um mehr zu sehen.',
  },
  error: {
    selectFirst: 'Bitte zuerst Anmeldedaten auswählen',
    clearFailed: 'Löschen fehlgeschlagen: ',
    batchFailed: 'Massenvorgang fehlgeschlagen: ',
    superAdminOnlyHint: 'Nur Super-Administratoren können Statuskorrekturen ausführen; derzeit nur lesend.',
    superAdminOnlyError: 'Nur Super-Administratoren können Statuskorrekturen ausführen.',
  },
}
