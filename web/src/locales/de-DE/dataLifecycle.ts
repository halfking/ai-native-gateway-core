export default {
  refresh: "Aktualisieren",
  loading: "Wird geladen…",
  storage: {
    migrateFailedWithReason: 'Migration konnte nicht abgeschlossen werden: {reason}. Bitte erneut versuchen.',
  },
  /**
   * Großfeldverwaltung (BlobManager).
   * ★ Leerzustand (2026-10-27, Inkrement 14 · „Leerzustand 5 Punkte“): vorher stand hier
   *   der Generalschlüssel `common.feedback.noData` („zurzeit sind noch keine Inhalte
   *   vorhanden“). Diese Seite hat **keinen Zeitfilter** ⇒ kein Zeitraum im Text
   *   (nichts behaupten, was die Seite nicht weiß).
   */
  blob: {
    emptyNoResult: 'Es gibt noch keine Datensätze mit Großfeldern.',
  },
  storageOverview: {
    summary: {
      databaseTitle: "PostgreSQL-Datenbank",
      databaseSubtitle: "Gesamtbedarf (Tabellen + Indizes + TOAST)",
      databaseMetric: "Datenbank",
      tablesMetric: "Tabellen",
      indexesMetric: "Indizes",
      toastMetric: "TOAST",
      diskTitle: "Lokale Festplatte",
      diskUsedOf: "Belegt / {total}",
      path: "Pfad",
      used: "Belegt",
      free: "Frei",
      columnarTitle: "Spaltenorientierter Speicher (citus_columnar)",
      enabled: "Aktiviert",
      notInstalled: "Nicht installiert",
      columnarTableCount: "Tabellenanzahl (inkl. Partitionen)",
      columnarTableCountMeta: "Tabellen mit columnar-Zugriffsmethode",
      columnarTotalColumns: "Gesamtanzahl Spalten",
      columnarTotalColumnsMeta: "Summe aller Spalten in columnar-Tabellen",
      columnarSize: "Columnar-Größe",
      columnarSizeMeta: "Komprimiert (inkl. Chunk-Metadaten)",
      columnarDbShare: "DB-Anteil",
      columnarDbShareMeta: "Columnar / gesamte Datenbankgröße",
      columnarNotInstalledFallback: "citus_columnar-Erweiterung ist nicht installiert",
      localLogsTitle: "Lokales Log-Verzeichnis",
      notFound: "Nicht gefunden",
      fileCount: "Dateien",
      size: "Größe",
      oldest: "Älteste",
      newest: "Neueste",
      localLogsMissingHint: "Log-Verzeichnis nicht gefunden; gzip-Backup / Rotation schreibt hier nicht.",
      warningsTitle: "Warnungen"
    },
    tablesTitle: "Top-{n} Datenbanktabellen",
    refresh: "Aktualisieren",
    refreshLoading: "Wird geladen…",
    columns: {
      tableName: "Tabelle",
      rows: "Zeilen",
      totalSize: "Gesamtgröße",
      indexes: "Indizes",
      toast: "TOAST",
      percent: "Anteil",
      partition: "Partition",
      actions: "Aktionen"
    },
    partitionBadge: "Partition",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table}: keine Sperre, gibt dead tuples frei",
      vacuumFullTitle: "VACUUM FULL {table}: Sperre, gibt Speicher an das BS zurück",
      reindexTitle: "REINDEX TABLE {table}: Sperre, baut Indizes neu auf"
    },
    modal: {
      vacuumTitle: "🧹 VACUUM bestätigen",
      vacuumFullTitle: "🗜 VACUUM FULL bestätigen",
      reindexTitle: "🔧 REINDEX bestätigen",
      intro: "{op} wird auf {table} ausgeführt.",
      currentSize: "Aktuelle Größe: ",
      rowCount: "Zeilenanzahl: ",
      partition: "Partitioniert: ",
      partitionYes: "Ja (⚠️ Sperre wirkt auf alle Kindpartitionen)",
      partitionNo: "Nein",
      flow: "Ablauf",
      impact: "Auswirkung",
      disk: "Auswirkung auf Speicherplatz",
      time: "Geschätzte Dauer",
      cancel: "Abbrechen",
      confirm: "{op} ausführen",
      success: "{op} {table} abgeschlossen: {size} freigegeben ({pct}%)",
      failed: "{op} fehlgeschlagen: {msg}"
    },
    rowResult: {
      running: "{op} läuft…",
      success: "{op}: {size} freigegeben ({pct}%, {ms}ms)",
      failed: "{op} fehlgeschlagen: {msg}"
    },
    op: {
      vacuum: {
        flow: "Durchsucht die Tabelle nach dead tuples und markiert deren Speicher als wiederverwendbar (wird noch nicht an das Betriebssystem zurückgegeben). Aktualisiert außerdem die Statistiken, damit der Planner bessere Pläne wählt.",
        impact: "Sperrt die Tabelle nicht; SELECT / INSERT / UPDATE / DELETE laufen normal weiter. Verbraucht während der Ausführung etwas zusätzliche CPU und IO.",
        disk: "Gibt den Speicher nicht sofort an das Betriebssystem zurück, sondern wird innerhalb der Tabelle wiederverwendet. Reduziert künftiges Wachstum, ändert pg_database_size aber nicht sofort.",
        time: "Meist Sekunden bis Minuten. Kosten ≈ Tabellengröße / sequentielle Lese-IO (≈200 MB/s auf einer typischen SSD)."
      },
      vacuumFull: {
        flow: "Holt eine ACCESS EXCLUSIVE-Sperre, schreibt die gesamte Tabelle in eine neue Datei und tauscht diese nach Abschluss aus, wobei die alte Datei freigegeben wird.",
        impact: "⚠️ Während die ACCESS EXCLUSIVE-Sperre gehalten wird, sind ALLE Lese- und Schreibzugriffe auf diese Tabelle blockiert. In verkehrsarmen Zeiten ausführen.",
        disk: "Die Speichernutzung verdoppelt sich vorübergehend (alt + neu). Nach Abschluss wird die alte Datei freigegeben. Maximal rückgewinnbarer Bloat ≈ Tabellen- + TOAST- + Indexgröße.",
        time: "Kosten ≈ (Tabelle + Indizes) / sequentielle Schreib-IO (≈200 MB/s auf einer typischen SSD). Beispiel: 10 GB ≈ 1–3 Min, 100 GB ≈ 15–30 Min."
      },
      reindex: {
        flow: "Holt eine Tabellensperre und baut alle unterstützten Indizes (z. B. B-tree) nacheinander neu auf. Nach Abschluss ersetzen die neuen Indizes die alten.",
        impact: "⚠️ DML (INSERT / UPDATE / DELETE) ist während des Vorgangs gesperrt. Empfohlen in verkehrsarmen Zeiten.",
        disk: "Die Indexgröße verdoppelt sich vorübergehend (alt + neu). Nach Abschluss werden die alten Indizes freigegeben. Entfernt Index-Bloat und kann nachfolgende Lesevorgänge beschleunigen.",
        time: "Kosten ≈ Gesamtgröße der Indizes / sequentielle Schreib-IO (≈200 MB/s auf einer typischen SSD). Diese Tabelle hat Indizes ≈ {size}; Sekunden bis Minuten."
      }
    }
  },
  hotPartition: {
    hotTableTitle: "Hot-Tabellen-Datenmigration",
    hotTableDesc: "Verschiebt Daten aus Hot-Tabellen in monatliche Partitionen. Erst ein anschließendes VACUUM FULL gibt den Speicher tatsächlich an das Betriebssystem zurück.",
    retention: {
      all: "Sofort alles migrieren",
      "1day": "Letzte 1 Tag behalten",
      "3day": "Letzte 3 Tage behalten",
      "7day": "Letzte 7 Tage behalten",
      "30day": "Letzte 30 Tage behalten"
    },
    startMigrate: "Migration starten",
    migrating: "Migriere…",
    migrationProgress: "{migrated} Zeilen migriert ({batches} Stapel, {duration}s)",
    promoteConfirm: "Daten aus {label} migrieren, die älter als {hours} sind?",
promoteAll: "alles",
   days: "{n} Tage",
    partitionTitle: "Monatliche Partitionsverwaltung",
    partitionDesc: "Alte monatliche Partitionen löschen, um Speicherplatz freizugeben.",
    partitionWarning: "Dieser Vorgang ist nicht umkehrbar: bitte mit Vorsicht.",
    tables: {
      title: "Partitionierte Tabellen",
      refresh: "Aktualisieren",
      columns: {
        name: "Tabelle",
        description: "Beschreibung",
        totalSize: "Gesamtgröße",
        rows: "Zeilen",
        partitions: "Partitionen",
        archivable: "Archivierbar",
        actions: "Aktionen"
      },
      viewPartitions: "Partitionen anzeigen / verwalten",
      noTables: "Keine partitionierten Tabellen"
    },
    partitionList: {
      title: "Partitionen von {table}",
      columns: {
        name: "Partition",
        storageType: "Speicher",
        size: "Größe",
        rows: "Zeilen",
        month: "Monat",
        action: "Aktionen"
      },
      storage: {
        columnar: "spaltenorientiert",
        heap: "Heap",
        archive: "archiviert"
      },
      delete: "Löschen",
      deleting: "Lösche…",
      empty: "Keine Partitionen für diese Tabelle"
    },
    deleteModal: {
      title: "Partition löschen bestätigen",
      warning: "Dieser Vorgang ist nicht umkehrbar!",
      fields: {
        name: "Partition",
        size: "Größe",
        rows: "Zeilen",
        month: "Monat"
      },
      inputLabel: "Partitionsnamen zur Bestätigung eingeben:",
      cancel: "Abbrechen",
      confirm: "Partition löschen",
      deleting: "Lösche…",
    },
    hotTableNames: {
      requestLogs: "Anfrage-Logs",
      credentialModelIndex: "Anmeldedaten-Modellindex",
      usageLedger: "Nutzungsjournal",
      routingDecisionLog: "Routing-Entscheidungsprotokoll"
    },
    labels: {
      rows: "Zeilen",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'Statistik der hot-Tabelle konnte nicht geladen werden: {reason}. Bitte erneut versuchen.',
    startMigrationFailedWithReason: 'Migrationsauftrag konnte nicht gestartet werden: {reason}. Bitte erneut versuchen.',
    submitDeleteFailedWithReason: 'Löschauftrag konnte nicht übermittelt werden: {reason}. Bitte erneut versuchen.',
  },
  preview: "Bereinigung vorschauen",
  hotData: "Hot-Daten (0-7 Tage)",
  warmData: "Warm-Daten (7-30 Tage)",
  coldData: "Cold-Daten (30-90 Tage)",
  expiredData: "Abgelaufene Daten (>90 Tage)",
  missingDate: "Wählen Sie ein Start- und ein Enddatum",
  previewFailed: 'Vorschau konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
  needPreview: "Prüfen Sie zuerst die Auswirkungen in der Vorschau",
  executeNotImpl: "Die Bereinigungsfunktion wird in einer späteren Phase implementiert. Führen Sie die Bereinigung über das Kommandozeilenskript aus."
,
  pages: {
    filesystemMaintenance: 'Dateisystem-Wartung',
    storageConfig: 'Speicherkonfiguration',
    logManagement: 'Protokollverwaltung',
    usageCost: 'Nutzungskosten',
    usageEnhanced: 'Erweiterte Nutzungsanalyse',
  },

  usageEnhanced: {
    refresh: 'Aktualisieren',
    loading: 'Wird geladen…',
    empty: 'Für diesen Zeitraum liegen keine Nutzungsdaten vor.',
    cards: {
      totalCost: 'Gesamtkosten',
      totalRequests: 'Gesamtzahl der Anfragen',
      periodDelta: 'Periodenvergleich Δ Kosten',
      cacheHitRate: 'Cache-Trefferquote',
    },
    trend: {
      title: 'Kostentrend (letzte {days} Tage)',
    },
    hotKeys: {
      title: 'Top-API-Schlüssel (nach Token-Verbrauch)',
      colId: 'API-Key-ID',
      colPrefix: 'Key-Präfix',
      colApplication: 'Anwendung',
      colRequests: 'Anfragen',
      colTokens: 'Token-Anzahl',
      colCost: 'Gesamtkosten',
    },
    degraded: {
      title: 'Einige Kennzahlen sind nicht verfügbar: ',
      hint: 'Das Datenbankschema ist älter als diese Version; die Werte sind Platzhalter für „nicht berechenbar“, keine echten Messwerte.',
    },
    errors: {
      load: 'Erweiterte Nutzungsdaten konnten nicht geladen werden. Bitte erneut versuchen.',
    },
    sources: {
      summary: 'Nutzungsübersicht',
      costTrend: 'Kostentrend',
      periodCompare: 'Periodenvergleich',
      cacheEconomics: 'Cache-Ökonomie',
      hotKeys: 'Hot-API-Schlüssel',
    },
    footer: 'Zuletzt aktualisiert: {time}',
  },

  usageCost: {
    refresh: 'Aktualisieren',
    loading: 'Wird geladen…',
    compare: {
      title: 'Kostenvergleich',
      currentPeriod: 'Aktuell:',
      previousPeriod: 'Vergleich:',
      currentLabel: 'Aktuell ({period})',
      previousLabel: 'Vergleichszeitraum ({period})',
      change: 'Änderung',
      significant: 'Signifikant',
      meta: '{requests} Anfragen: {models} Modelle',
    },
    attribution: {
      title: 'Kostenzuordnung',
      groupBy: 'Gruppieren nach:',
      model: 'Nach Modell',
      provider: 'Nach Anbieter',
      intent: 'Nach Intent',
      shareChart: 'Kostenanteil',
      detailChart: 'Kostenaufschlüsselung (Input vs Output)',
      otherNote: 'Weitere {count} Einträge gesamt: {cost}',
      otherLabel: 'Sonstige',
      colModel: 'Modell',
      colProvider: 'Anbieter',
      colIntent: 'Intent',
      colRequests: 'Anfragen',
      colTotalCost: 'Gesamtkosten',
      colInputCost: 'Input-Kosten',
      colOutputCost: 'Output-Kosten',
      colShare: 'Anteil',
      colLatency: 'Ø Latenz',
    },
    charts: {
      inputCost: 'Input-Kosten',
      outputCost: 'Output-Kosten',
    },
    cache: {
      title: 'Cache-Ökonomie',
      hitRate: 'Cache-Trefferquote',
      cacheSaved: 'Cache-Einsparung',
      cacheSavedHint: 'vs. ohne Cache',
      compressionSaved: 'Kompressions-Einsparung',
      compressionCount: '{n} Kompressionen',
      savingsRate: 'Gesamteinsparungsrate',
      totalSaved: 'Gespart {cost}',
      actualSpend: 'Tatsächliche Ausgaben',
      effectiveRatio: '{pct} der unoptimierten Kosten',
      totalRequests: 'Anfragen gesamt',
    },
    errors: {
      costTrend: 'Kostentrend konnte nicht geladen werden. Bitte erneut versuchen.',
      periodCompare: 'Periodenvergleich konnte nicht geladen werden. Bitte erneut versuchen.',
      cacheEconomics: 'Cache-Ökonomie konnte nicht geladen werden. Bitte erneut versuchen.',
    },
    degraded: {
      title: 'Einige Kennzahlen sind nicht verfügbar:',
      hint: 'Das Datenbankschema ist hinter diesem Build zurück, die Werte unten sind also Platzhalter für „nicht berechenbar“ – keine Messwerte (das ist nicht dasselbe wie keine Daten für diesen Zeitraum). Bitte einen Administrator, die Datenaggregationsmigration auszuführen.',
    },
  },
  confirmDeleteCleanup: '{rows} Zeilen löschen?\nGeschätzter freigegebener Speicher: {size}\n\nDiese Aktion ist unwiderruflich!',
  confirmArchiveCleanup: '{rows} Zeilen archivieren?\nGeschätzter freigegebener Speicher: {size}\n\nDiese Aktion ist unwiderruflich!',
  confirmAttachmentNullify: 'Ausführen?\n{n} Elements werden auf NULL gesetzt\nGesamt: {size}\n(physische Dateien im Dateisystem werden nicht gelöscht)',
  confirmBlobCleanup: 'Bereinigen?\n{rows} Zeilen betroffen\nca. {size} freigegeben\n(nur request_body / outbound_body werden NULL; Metadaten bleiben)',
  confirmReplay: '{filename} wiedergeben?',
  confirmReplayArchive: '{filename} wiedergeben und archivieren?',
  confirmFileCleanup: 'Dateien älter als {days} Tage löschen?\nGrund: {reason}',
  confirmLogCleanup: 'Logdateien älter als {days} Tage löschen? Dies kann nicht rückgängig gemacht werden.',
  confirmDirMigrate: 'Das Speicherverzeichnis für Anhänge wird geändert. Bestehende Dateien werden ins neue Verzeichnis kopiert; das alte wird nach Prüfung gelöscht.\n\n· Downloads/Schreibzugriffe sind während des Kopierens nicht betroffen (weiterhin altes Verzeichnis)\n· Wechselt nach Abschluss automatisch zum neuen Verzeichnis\n· Bei vielen Dateien kann es dauern; der Fortschritt erscheint unten\n\nVerzeichnis ändern und migrieren?',
  dirMigrateCancelled: 'Abgebrochen (Verzeichnis unverändert)',
}