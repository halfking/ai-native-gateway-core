// freeDiscovery.ts — free discovery page copy (de-DE).
export default {
  page: {
      title: "Discovery kostenloser Ressourcen",
      desc: "Anbietervorlagen konfigurieren → Upstream-Modelllisten scannen → prüfen → in den kostenlosen Ressourcenpool importieren ( angebunden an OmniFree-Quotenverfolgung)."
    },
  common: {
      refresh: "Aktualisieren",
      loading: "Wird verarbeitet…",
      empty: "Keine Daten",
      actions: "Aktionen",
      enabled: "Aktiviert",
      disabled: "Deaktiviert",
      hideForm: "Einklappen"
    },
  tabs: {
      templates: "Vorlagen",
      tasks: "Aufgaben & Prüfung",
      history: "Importverlauf"
    },
  presets: {
      title: "Integrierte Anbieter-Presets",
      create: "Erstellen",
      exists: "Vorhanden",
      createDone: "Vorlage aus Preset erstellt: {name}",
      scannerPending: "Scanner für dieses Protokoll ist in Arbeit; Scans schlagen derzeit fehl",
      keyless: "keyless (ohne API-Key)"
    },
  form: {
      show: "+ Eigene Vorlage",
      providerCode: "Provider Code *",
      displayName: "Anzeigename",
      baseUrl: "Base URL *",
      apiType: "API-Protokoll",
      apiKeyEnv: "API-Key-Umgebungsvariable",
      apiKeyEnvHint: "Der Key selbst wird nicht gespeichert: $VAR-Umgebungsreferenz verwenden (z. B. $GROQ_API_KEY); leer lassen für keyless.",
      tosVerdict: "ToS-Einschätzung",
      submit: "Vorlage erstellen",
      created: "Vorlage erstellt"
    },
  orbi: {
      show: "Orbi-Vorlagen-JSON importieren",
      hint: "Inhalt einer Orbi-pi-providers-Vorlagendatei einfügen (JSON mit Top-Level-providers-Schlüssel). Eine Datei kann mehrere Provider enthalten.",
      import: "Importieren",
      invalidJson: "JSON konnte nicht geparst werden, bitte Format prüfen",
      done: "Import abgeschlossen: {created} erstellt, {failed} fehlgeschlagen"
    },
  tpl: {
      listTitle: "Vorlagen ({n})",
      name: "Vorlage",
      baseUrl: "Base URL",
      apiType: "API-Protokoll",
      keyEnv: "Key-Umgebungsvariable",
      tos: "ToS",
      enabled: "Aktiv",
      createdAt: "Erstellt am",
      scan: "Scannen",
      delete: "Löschen",
      deleteConfirm: "Vorlage \"{name}\" löschen? Bestehende Discovery-Aufgaben und -Ergebnisse bleiben unberührt.",
      deleted: "Vorlage gelöscht: {name}"
    },
  scan: {
      title: "Discovery auslösen",
      pickTemplate: "Aktivierte Vorlage wählen…",
      start: "Scan starten",
      running: "Scan läuft…",
      done: "Scan abgeschlossen: {n} Modelle gefunden, bitte Ergebnisse prüfen",
      failed: "Scan fehlgeschlagen"
    },
  task: {
      listTitle: "Discovery-Aufgaben ({n})",
      provider: "Anbieter",
      status: "Status",
      trigger: "Auslöser",
      found: "Gefunden",
      imported: "Importiert",
      by: "Von",
      time: "Erstellt am",
      error: "Fehler",
      review: "Ergebnisse prüfen"
    },
  res: {
      title: "Ergebnisse · Aufgabe {id} · {provider}",
      pending: "Zu prüfen",
      all: "Alle",
      model: "Modell-ID",
      displayName: "Anzeigename",
      freeType: "Free-Typ",
      monthly: "Monatsquote (tokens)",
      daily: "Tagesquote (tokens)",
      tos: "ToS",
      importStatus: "Importstatus",
      none: "Keine Ergebnisse für diesen Filter",
      policy: "Konfliktstrategie",
      policySkip: "skip: bestehende Einträge behalten",
      policyOverwrite: "overwrite: überschreiben und reaktivieren",
      policyMerge: "merge: nur leere Felder ergänzen",
      importSelected: "Auswahl importieren ({n})",
      importAllPending: "Alle offenen importieren",
      importedToast: "Import abgeschlossen: {imported} importiert, {skipped} übersprungen, {conflicted} Konflikte, {failed} fehlgeschlagen"
    },
  status: {
      taskPending: "Wartend",
      running: "Läuft",
      success: "Erfolg",
      failed: "Fehler",
      review: "Offen",
      imported: "Importiert",
      skipped: "Übersprungen",
      conflict: "Konflikt"
    },
  trigger: {
      manual: "Manuell",
      scheduled: "Geplant",
      webhook: "Webhook"
    },
  hist: {
      title: "Importverlauf ({n})",
      desc: "Aufgaben mit tatsächlichen Importen (Importanzahl > 0). Über Details sehen Sie, wo jedes Ergebnis gelandet ist.",
      completed: "Abgeschlossen am",
      detail: "Details",
      none: "Noch keine Importe. Nach einem Bulk-Import unter Aufgaben & Prüfung erscheint er hier.",
      detailTitle: "Importdetails · Aufgabe {id} · {provider}",
      importedAt: "Importiert am"
    },
}
