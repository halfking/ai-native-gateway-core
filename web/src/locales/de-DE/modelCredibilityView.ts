// modelCredibilityView.ts — Modellglaubwürdigkeit (Seite de-DE).
export default {
  pageTitle: 'Modellglaubwürdigkeit',
  pageSubtitle:
    'Prüft, ob ein Anbieter ein kleineres Modell als ein größeres verkauft ' +
    '(Austausch, Downgrade, Verwässerung). Das Urteil stützt sich auf vier unabhängige Messungen: ' +
    'die Selbstauskunft des Anbieters, den Fähigkeitsvergleich mit anderen Knoten desselben Modells, ' +
    'die Stabilität der Anfrage-Signatur und eine externe Referenz. Lässt sich nichts ableiten, ' +
    'steht „nicht genug Belege", niemals „kein Problem".',

  stats: {
    total: 'Bewertete Knoten',
    verified: 'Bestätigt',
    suspected: 'Verdächtig',
    diluted: 'Verwässert',
    unknown: 'Nicht genug Belege',
    providers: 'Beteiligte Anbieter',
  },

  filter: {
    verdict: 'Urteil',
    verdictPlaceholder: 'Alle Urteile',
    model: 'Modell',
    modelPlaceholder: 'Nach Modellnamen filtern',
    query: 'Abfragen',
    refresh: 'Aktualisieren',
    scanAll: 'Alles prüfen',
    scanning: 'Prüfung läuft…',
    sort: 'Sortierung',
    sortTime: 'Nach Messzeitpunkt (neueste zuerst)',
    sortName: 'Nach Modellname',
    sortScore: 'Nach Glaubwürdigkeit (aufsteigend)',
  },

  verdict: {
    verified: 'Bestätigt',
    suspected: 'Verdächtig',
    diluted: 'Verwässert',
    unknown: 'Nicht genug Belege',
  },

  dimension: {
    identity: 'Identität',
    capability: 'Fähigkeit',
    consistency: 'Signatur-Stabilität',
    reference: 'Externe Referenz',
  },

  table: {
    provider: 'Anbieter',
    credential: 'Zugangsdaten',
    model: 'Modell',
    credibility: 'Glaubwürdigkeit',
    verdict: 'Urteil',
    dimensions: 'Belegdimensionen',
    measuredAt: 'Gemessen am',
    action: 'Aktionen',
  },

  status: {
    success: 'Einwandfrei',
    partial: 'Teilweise',
    failed: 'Sonde fehlgeschlagen',
    inconclusive: 'Nicht aussagekräftig',
    unknown: 'Nicht gemessen',
  },

  anomaly: {
    identity_model_name_mismatch: 'Der Anbieter meldet ein anderes Modell als das angeforderte',
    identity_family_mismatch: 'Der Anbieter meldet ein Modell eines anderen Herstellers',
    identity_reported_model_absent: 'Der Anbieter nennt keinen Modellnamen, Identität ist nicht belegbar',
    capability_below_peer_best: 'Fähigkeit unter dem besten Knoten desselben Modells',
    capability_severely_below_peer_best: 'Fähigkeit deutlich unter dem besten Knoten desselben Modells',
    capability_above_peer_best: 'Fähigkeit über dem besten Knoten desselben Modells',
    consistency_prompt_token_drift: 'Anfrage-Signatur driftet; das Backend wurde möglicherweise getauscht',
    consistency_system_fingerprint_drift: 'Der Fingerabdruck des Anbieters hat sich geändert',
    reference_below_expected_accuracy: 'Unter dem Niveau, das die externe Referenz erwartet',
    capability_no_measurement: 'Keine verwertbare Fähigkeitsmessung vorhanden',
    capability_peer_sample_too_small: 'Zu wenige Vergleichsknoten für dieses Modell',
    identity_probe_failed: 'Die Identitätssonde ist fehlgeschlagen',
    identity_probe_empty_response: 'Die Identitätssonde hat keine Antwort erhalten',
  },

  detail: {
    title: 'Urteilsdetails',
    evidence: 'Belege',
    anomalies: 'Auffälligkeiten',
    history: 'Urteilsverlauf',
    noHistory: 'Noch kein Urteilsverlauf',
    requestedModel: 'Angefordertes Modell',
    reportedModel: 'Vom Anbieter gemeldetes Modell',
    promptSignature: 'Anfrage-Signatur (Prompt-Token-Zahl)',
    fingerprint: 'Fingerabdruck des Anbieters',
    measuredDimensions: 'Dimensionen mit Daten in dieser Runde',
    close: 'Schließen',
  },

  empty: {
    noData: 'Noch keine Glaubwürdigkeitsdaten.',
    hint: 'Die Urteile entstehen durch den periodischen Hintergrundscan (je Knoten eine kleine Sonde pro Runde); „Alles prüfen" startet sofort eine Runde.',
  },

  error: {
    loadFailed: 'Die Glaubwürdigkeitsdaten der Modelle konnten nicht geladen werden. Bitte erneut versuchen.',
    scanFailed: 'Die Glaubwürdigkeitsprüfung konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
    detailFailed: 'Die Details zum Glaubwürdigkeitsurteil konnten nicht geladen werden. Bitte erneut versuchen.',
  },
}
