export default {
  // Allgemein
  login: 'Anmelden',
  logout: 'Abmelden',
  changePassword: 'Passwort ändern',
  cancel: 'Abbrechen',
  confirm: 'Bestätigen',
  save: 'Speichern',
  delete: 'Löschen',
  edit: 'Bearbeiten',
  search: 'Suchen',
  reset: 'Zurücksetzen',
  submit: 'Senden',
  back: 'Zurück',
  next: 'Weiter',
  previous: 'Zurück',
  close: 'Schließen',
  
  // Benutzerrollen
  role: {
    super_admin: 'Super-Administrator',
    tenant_admin: 'Mandanten-Administrator',
  },
  
  // Navigation
  nav: {
    collapseSidebar: 'Menü einklappen',
    expandSidebar: 'Seitenleiste erweitern',
  },
  
  // Passwort
  password: {
    changeSuccess: 'Passwort erfolgreich geändert',
    changeFailed: 'Passwortänderung fehlgeschlagen',
  },
  
  // Versionsinformationen
  version: 'Version',
  build: 'Build',

  // 2026-07-02 (request-logs Anhangsansicht): Anhangsbezogene Texte,
  // entsprechend Referenzdokument §6.
  requests: {
    list: {
      table: {
        attachmentsTitle: 'Anhänge',
        noAttachments: 'Keine Anhänge',
      },
    },
    detail_extra: {
      attachmentsTab: 'Anhänge',
      attachmentsLoading: 'Anhänge werden geladen…',
      noAttachments: 'Keine Anhänge',
      clickToPreviewTitle: 'Klicken zur Vorschau',
      download: 'Herunterladen',
      downloadOriginal: 'Original herunterladen',
      closePreview: 'Schließen',
    },
  },

  // 2026-07-03 (Echtzeit-Anfrage-Stream): Dashboard swim lane.
  dashboard: {
    liveStream: {
      title: 'Echtzeit-Anfrage-Stream',
      connected: 'Verbunden',
      connecting: 'Verbinde…',
      reconnecting: 'Verbinde erneut…',
      disconnected: 'Getrennt',
      unsupported: 'Nicht unterstützt',
      pause: 'Pause',
      resume: 'Fortsetzen',
      filterAll: 'Alle Status',
      filterSuccess: 'Nur Erfolge',
      filterInProgress: 'In Bearbeitung',
      filterGroupFailures: 'Fehleraufschlüsselung',
      filterFailure5xx: 'Server / Upstream (5xx)',
      filterFailure4xx: 'Client / Auth (4xx)',
      filterFailureTimeout: 'Timeout / Netzwerk',
      filterFailureNotFound: 'Routing / Modell nicht gefunden',
      filterFailureOther: 'Sonstige Fehler',
      empty: 'Warte auf Anfragen…',
      countTooltip: '{buffer} im Puffer / {visible} sichtbar',
      countAria: '{buffer} Anfragen im Puffer, {visible} sichtbar',
      legend: {
        model: 'Modellfamilie',
        status: 'Status',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: 'Inländisch',
        oss: 'Open Source',
        other: 'Sonstige',
        success: 'Erfolg',
        inProgress: 'In Bearbeitung',
        failure: 'Fehler',
      },
    },
  },
}