// liveStageTree.ts — Anfrage-Stufenbaum (live2) Texte (2026-11-16)
export default {
  title: 'Anfrage-Stufenbaum',
  empty: 'Im gewählten Zeitfenster gibt es unter den aktuellen Filtern keine Anfragen',
  stage: {
    auth: 'Auth / Ratenschutz',
    sanitize: 'Schwärzung',
    approval: 'Freigabe',
    injection: 'Injection-Prüfung',
    route: 'Weiterleitung',
    compress: 'Komprimierung',
    firstByte: 'Erstes Byte',
    restore: 'Maskierungs-Rückführung',
    provider: 'Anbieter / Token-Pool',
    meter: 'Verrechnung',
    reply: 'Antwort',
  },
  state: {
    pending: 'Nicht erreicht',
    active: 'Läuft',
    done: 'Abgeschlossen',
    skipped: 'Nicht zutreffend',
    failed: 'Fehlgeschlagen',
    warned: 'Warnung',
    unmeasured: 'Nicht gemessen',
  },
  value: {
    sanitize: '{n} Stelle(n)',
    route: '{n} Kandidat(en)',
    meter: '{n} Token',
    injection: 'Score {n} ({d})',
    compress: {
      yes: 'Komprimiert',
      no: 'Nicht komprimiert',
    },
  },
  decision: {
    pass: 'Freigegeben',
    need_approval: 'Wartet auf Freigabe',
    block: 'Abgelehnt',
    safe: 'Sicher',
    warn: 'Treffer (nur Warnung)',
    unknown: 'Nicht bestimmt',
  },
  return: {
    lane: 'Die Antwort läuft über den Rückweg zum Client',
    label: 'Zum Client',
  },
  band: {
    status: {
      success: 'Erfolgreich',
      in_progress: 'Läuft',
      failure: 'Fehlgeschlagen',
    },
    toolsRestore: 'tools rückgeführt {n}',
    toolsRestored: 'zurückgeführt',
    finalModel: 'Endgültiges Modell',
    upstreamModel: 'Upstream-Modell',
    finalModelTip: {
      action: 'Quelle: Routing-Ereignis der gewählten Node-Aktion (Standardmodellname nach der Auflösung).',
      canonical: 'Quelle: der auf dem Anfrage-Datensatz protokollierte Standardmodellname.',
      upstreamRaw: 'Quelle: die tatsächlich an den Upstream gesendete Modellzeichenfolge.',
      frame: 'Quelle: das Modellfeld des Anfrage-Datensatzes.',
      none: 'Nicht gemessen: keine bestimmbare Modellquelle.',
    },
    modelEntry: {
      auto: 'AUTO wählte {model}',
      direct: 'Angefragt {model}',
    },
    chain: {
      auto: 'auto-Aufgabentyp {task} → getroffen {model}',
      alias: '{from} wurde zu {model} aufgelöst',
      switch: 'Modellwechsel {from} → {to}',
    },
    cache: {
      hit: 'Cache-Treffer {pct}%',
      na: 'Cache-Trefferquote —',
      anomaly: 'Cache-Basis inkonsistent',
      tip: {
        na: 'Der Upstream hat keine Cache-Trefferzahl gemeldet; das ist nicht 0%.',
        unknown: 'Das Upstream-Protokoll ist unbestimmt, der Nenner ist damit unbekannt.',
        anomaly: 'Die Quote liegt über 100%, das bedeutet zwei vermischte Basen.',
        parallel: 'Basis: Cache-Lesungen stehen neben der Eingabe; Nenner = Eingabe + Cache-Lesungen.',
        subset: 'Basis: Cache-Lesungen stecken bereits in der Eingabe; Nenner = Eingabe.',
      },
    },
  },
  credibility: {
    verified: 'Vertrauenswürdig',
    suspected: 'Verdächtig verdünnt',
    diluted: 'Verdünnt',
    unknown: 'Unbestimmt',
    unmeasured: 'Vertrauenswürdigkeit nicht gemessen',
    fingerprintMissing: 'Nicht bereitgestellt',
    measuredAtMissing: 'Nicht bereitgestellt',
    tip: {
      unmeasured: 'Für diese Kombination aus Zugangsdaten und Upstream-Modell gibt es keine Messung.',
      detail:
        'Urteil {verdict} | Identität {identity} | Fähigkeit {capability} | Stabilität {consistency} | Referenz {reference} | System-Fingerabdruck {fingerprint} | gemessen am {measuredAt}',
    },
  },
  stats: {
    open: 'Statistiken aufklappen (ganze Seite)',
    readout: '{total} gesamt | {rate}% Erfolg | {inFlight} in Arbeit',
    title: 'Live-Statistik',
    hint:
      'Gleiche Basis wie die Live-Topologie-Seite: die letzten 5 Minuten und die letzte Stunde werden direkt aus der Datenbank gelesen, der laufende Tag kommt aus dem Tages-Bucket (bis zu 1 Stunde verzögert).',
    close: 'Schließen',
  },
  idle: {
    wait: 'Seit {ms} ms keine neue Anfrage',
  },
}
