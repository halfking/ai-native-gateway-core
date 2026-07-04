// German locale for MemoraStatusButton
export default {
  state: {
    disabled: 'Deaktiviert',
    paused: 'Getrennt',
    ok: 'Verbunden',
    error: 'Verbindung fehlgeschlagen',
    loading: 'Überprüfung läuft',
  },
  
  panel: {
    title: 'Memora-Verbindung',
    closeLabel: 'Schließen',
    
    fields: {
      serviceUrl: 'Dienst-URL',
      recentLatency: 'Aktuelle Latenz',
      error: 'Fehler',
      check: 'Prüfung',
      writeQueue: 'Schreibwarteschlange',
      processedErrored: 'Verarbeitet / Fehlgeschlagen',
      consecutiveErrors: 'Aufeinanderfolgende Fehler',
      recentWriteError: 'Letzter Schreibfehler',
    },
    
    actions: {
      processing: 'Wird verarbeitet…',
      reconnect: 'Erneut verbinden',
      checkConnectivity: 'Konnektivität prüfen',
      disconnect: 'Schreiben trennen',
      refresh: 'Aktualisieren',
      sessionContext: 'Sitzungskontext',
    },
  },
  
  error: {
    connectionFailed: 'Verbindung fehlgeschlagen',
    checkFailed: 'Prüfung fehlgeschlagen',
  },
}
