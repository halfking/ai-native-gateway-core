// selfUpdateView.ts — Selbstaktualisierung und Upgrade (SelfUpdateView).
//
// Formulierungsregel (gleiche Grundlage wie im Seitencode): Pruefung
// fehlgeschlagen, keine entfernte Release-Quelle konfiguriert oder noch
// nie geprueft bedeutet "Ergebnis nicht feststellbar". Dann nennt die Seite
// den Status als unbekannt und nennt den Grund, sie behauptet nie "bereits
// auf dem neuesten Stand".
export default {
  pageTitle: 'Selbstaktualisierung und Upgrade',
  pageSubtitle:
    'Der Knoten sucht selbstständig nach neuen Releases, erzeugt eine Erinnerung und installiert das Upgrade, nachdem eine Administratorin oder ein Administrator es bestätigt hat',

  statusCardTitle: 'Aktueller Release-Status',
  statusCardHint:
    'Zeigt das auf diesem Knoten laufende Release und das Ergebnis der letzten Prüfung auf entfernte Releases',
  currentVersion: 'Aktuelle Version',
  latestVersion: 'Neueste Version',
  buildSequence: 'Build-Sequenz',
  lastCheckedAt: 'Zuletzt geprüft',
  checkInterval: 'Prüfintervall',
  checkIntervalHours: 'Prüft automatisch alle {hours} Stunden',
  checkIntervalMinutes: 'Prüft automatisch alle {minutes} Minuten',
  checkIntervalSeconds: 'Prüft automatisch alle {seconds} Sekunden',
  checkNow: 'Jetzt auf Updates prüfen',
  checking: 'Wird geprüft',

  stateNotConfiguredLabel: 'Keine entfernte Release-Quelle konfiguriert',
  stateNotConfiguredDetail:
    'Auf diesem Knoten ist keine entfernte Release-Quelle konfiguriert, daher sind automatische Prüfung und Erinnerung abgeschaltet. Ob ein neuer Release existiert, lässt sich nicht feststellen.',
  stateUnknownLabel: 'Release-Status unbekannt',
  stateUnknownDetail:
    'Die Release-Prüfung hat kein Ergebnis geliefert: {reason}. Bis eine Prüfung erfolgreich ist, lässt sich nicht feststellen, ob ein neuer Release existiert.',
  stateNeverCheckedDetail:
    'Auf diesem Knoten ist eine entfernte Release-Quelle konfiguriert, aber es wurde noch keine Release-Prüfung ausgeführt. Ob ein neuer Release existiert, lässt sich nicht feststellen.',
  stateUpToDateLabel: 'Bereits auf dem neuesten Stand',
  stateUpToDateDetail:
    'Die entfernte Prüfung hat bestätigt, dass kein neuer Release verfügbar ist.',
  stateAvailableLabel: 'Ein neuer Release ist verfügbar',
  stateAvailableDetail:
    'Die entfernte Prüfung hat bestätigt, dass Release {version} verfügbar ist und installiert werden kann.',

  bannerTitle: 'Ein neuer Release ist verfügbar',
  bannerMandatory: 'Verpflichtendes Update',
  bannerOptional: 'Optionales Update',
  bannerNotesTitle: 'Versionshinweise',
  bannerNoNotes: 'Zu diesem Release wurden keine Versionshinweise hinterlegt.',
  installNow: 'Jetzt upgraden',

  confirmInstallTitle: 'Upgrade auf {version} bestätigen',
  confirmInstallBody:
    'Das Upgrade lädt das neue Binärprogramm herunter, prüft es, ersetzt das aktuelle Binärprogramm und startet den Dienst neu. Während des Neustarts werden Anfragen kurz unterbrochen. Fortfahren?',
  confirmInstallButton: 'Upgrade bestätigen',
  installStartFailed: 'Die Installationsaufgabe konnte nicht gestartet werden: {reason}',

  installCardTitle: 'Installationsfortschritt',
  installCardHint:
    'Zeigt Status und ausgeführte Schritte der letzten Installationsaufgabe',
  installCardEmpty: 'Auf diesem Knoten ist noch keine Installationsaufgabe erfasst.',
  jobVersion: 'Zielversion',
  jobMode: 'Installationsart',
  jobModeUnknown: 'Unbekannte Installationsart',
  jobStatus: 'Aufgabenstatus',
  jobStatusPending: 'Wartet auf Start',
  jobStatusDownloading: 'Lädt Artefakt herunter',
  jobStatusReadyToRestart: 'Wartet auf Dienstneustart',
  jobStatusUpgrading: 'Ersetzt Binärprogramm',
  jobStatusSuccess: 'Upgrade erfolgreich',
  jobStatusFailed: 'Upgrade fehlgeschlagen',
  jobStatusRolledBack: 'Auf vorherige Version zurückgerollt',
  jobStatusUnknown: 'Unbekannter Status',
  jobStartedAt: 'Startzeit',
  jobStepsTitle: 'Ausgeführte Schritte',
  jobStepsEmpty: 'Die Aufgabe hat noch keinen Schritt erzeugt.',
  jobErrorTitle: 'Fehlerursache',
  jobSuccessNotice: 'Das Upgrade ist abgeschlossen, die neue Version ist aktiv.',
  jobRestartScheduled:
    'Das Upgrade ist abgeschlossen, der Dienst startet gleich neu.',
  modeArtifact: 'Artefakt herunterladen und installieren',
  modeSource: 'Aus lokalem Quelltext bauen und installieren',

  notificationsTitle: 'Upgrade-Erinnerungen',
  notificationsHint:
    'Zeigt die Erinnerungen, die beim Finden eines neuen Releases erzeugt wurden',
  notificationsEmpty: 'Es wartet keine Upgrade-Erinnerung auf Bearbeitung.',
  notificationsLoadFailed:
    'Die Liste der Erinnerungen konnte nicht gelesen werden: {reason}',
  notificationVersion: 'Version',
  notificationStatus: 'Erinnerungsstatus',
  notificationStartedAt: 'Erzeugt am',
  notificationCompletedAt: 'Bearbeitet am',
  notificationRetryCount: 'Wiederholungen',
  notificationPending: 'Wartet auf Bearbeitung',
  notificationHandled: 'Bearbeitet',

  sourceCardTitle: 'Aus lokalem Quelltext bauen und upgraden',
  sourceCardHint:
    'Baut das Binärprogramm aus dem Quelltext auf diesem Rechner und installiert es. Der Build lässt den aktuellen Arbeitsbaum unverändert.',
  sourceRefLabel: 'Quelltext-Ref',
  sourceRefPlaceholder:
    'Leer lassen, um den aktuellen Arbeitsbaum zu verwenden',
  sourceBuildNow: 'Aus Quelltext bauen und upgraden',
  confirmSourceBuildTitle: 'Build aus Quelltext und Upgrade bestätigen',
  confirmSourceBuildBody:
    'Der Build holt einen eigenen Arbeitsbaum aus, erstellt das Binärprogramm, ersetzt die aktuelle Version und startet anschließend den Dienst neu. Dabei werden CPU und Datenträger dieses Rechners belegt. Fortfahren?',
  confirmSourceBuildButton: 'Build und Upgrade bestätigen',
  sourceBuildNoVersion:
    'Die zu installierende Version ist noch nicht bekannt. Führen Sie zuerst eine Release-Prüfung aus.',
  sourceBuildFailed: 'Die Build-Aufgabe konnte nicht gestartet werden: {reason}',
}
