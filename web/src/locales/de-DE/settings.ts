// Auto-translated draft (de-DE) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — SettingsView copy. Namespace: category / list / detail / editor / docs / errors.
// Technical field names (JSON / RPM / TPM / WebSocket) remain untranslated.
export default {
  category: {
    all: 'Alle',
    compression: 'Komprimierung',
    rateLimit: 'Ratenbegrenzung',
    timeout: 'Zeitüberschreitung',
    retry: 'Wiederholung & Timeout',
    routing: 'Routing',
    session: 'Sitzung',
    security: 'Sicherheit',
    outputCompliance: 'Ausgabe-Compliance',
    circuitBreaker: 'Schutzschalter',
    general: 'Andere',
  },
  dangerLevel: {
    note: '🟡 Hinweis',
    warn: '🟠 Warnung',
    danger: '🔴 Gefahr',
  },
  detail: {
    errors: {
      loadListFailed: 'Einstellungsliste konnte nicht geladen werden. Bitte erneut versuchen.',
      loadDetailFailed: 'Einstellungsdetails konnten nicht geladen werden. Bitte erneut versuchen.',
      saveFailed: 'Einstellungen konnten nicht gespeichert werden. Bitte erneut versuchen.',
      rollbackFailed: 'Rollback konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
      invalidNumber: 'Bitte geben Sie eine gültige Zahl ein',
    },
  },
  editor: {
    rollbackConfirm: '{key} auf den vorherigen Wert zurückrollen?',

    enabledText: 'Aktiviert',
    disabledText: 'Deaktiviert',
    saving: 'Wird gespeichert…',
    save: 'Speichern',
  },
  compression: {
    enumLabels: {
      off: '0 - Aus',
      auto: '1 - Auto-Schwelle',
      on4xx: '2 - Bei 4xx',
      smart: 'Smart',
      aggressive: 'Aggressiv',
    },
    enumDescriptions: {
      off: 'Komprimierung vollständig deaktiviert.',
      auto: 'Automatisch komprimieren, wenn die Nachrichtenlänge die Kontextfenster-Schwelle überschreitet.',
      on4xx: 'Bei einem 4xx-Fehler komprimieren und erneut versuchen (z. B. context_length_exceeded).',
    },
    strategyEnum: {
      naive: 'naive - Naive Komprimierung',
      smart: 'smart - Intelligente Komprimierung',
      adaptive: 'adaptive - Adaptive Komprimierung',
    },
  },
  docs: {
    compressionModeTitle: '📖 Komprimierungsmodus im Detail',
    compressionModeContent: `<p><strong>Komprimierungsmodus</strong> steuert, wie das System mit übergroßen Konversationen umgeht:</p>
<ul>
  <li><code>0 (off)</code> - Komprimierung deaktivieren; bei Kontextüberlauf einen Fehler zurückgeben.</li>
  <li><code>1 (auto_threshold)</code> - Vorhersagemodus; proaktiv komprimieren, wenn Nachrichten sich dem Kontextfenster nähern.</li>
  <li><code>2 (on_4xx)</code> - Reaktiver Modus; nach einem 4xx-Fehler komprimieren und erneut versuchen. [Empfohlen]</li>
</ul>
<p class="docs-note">💡 <strong>Modus 2 wird empfohlen</strong>: nur bei Bedarf komprimieren, um unnötigen Performance-Overhead zu vermeiden.</p>`,
    cacheEnabledTitle: '📖 Sitzungs-Cache im Detail',
    cacheEnabledContent: `<p><strong>Sitzungs-Cache</strong> steuert, ob der L1/L2/L3-Stufen-Cache aktiviert ist:</p>
<ul>
  <li><strong>L1</strong> - In-Memory-Cache (am schnellsten)</li>
  <li><strong>L2</strong> - Redis-Cache (mittel)</li>
  <li><strong>L3</strong> - Datenbank-Cache (am langsamsten)</li>
</ul>
<p class="docs-note">⚠️ Wenn deaktiviert, wird kein Sitzungsstatus gespeichert; dies beeinträchtigt die Kontextkontinuität.</p>`,
    formatConversionTitle: '📖 Formatkonvertierung im Detail',
    formatConversionContent: `<p><strong>Formatkonvertierung</strong> ermöglicht automatische Anfrageformatkonvertierung zwischen Protokollen:</p>
<ul>
  <li><strong>Q2-Pfad</strong>: Anthropic-Format → OpenAI-Modelle</li>
  <li><strong>Q3-Pfad</strong>: OpenAI-Format → Anthropic-Modelle</li>
</ul>
<p class="docs-note">💡 Anbieter-Ebene-Überschreibungen werden unterstützt; die Konvertierung kann für bestimmte Anbieter deaktiviert werden.</p>`,
    rateLimitRpmTitle: '📖 RPM-Ratenbegrenzung im Detail',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> begrenzt die Anzahl der Anfragen pro Mandant pro Minute:</p>
<ul>
  <li>Geeignet für grobgranulare Verkehrssteuerung</li>
  <li>Verwendet einen Sliding-Window-Algorithmus, sekundengenau</li>
  <li>Gibt HTTP 429 bei Überschreitung zurück</li>
</ul>
<p class="docs-note">⚠️ <strong>Mandantenbezogen</strong>: Diese Einstellung erfordert eine tenant_id; konfigurieren Sie sie auf der Mandantenverwaltungsseite.</p>`,
    rateLimitConcurrentTitle: '📖 Parallelitäts-Ratenbegrenzung im Detail',
    rateLimitConcurrentContent: `<p><strong>Parallelitäts-Ratenbegrenzung</strong> begrenzt die Anzahl der gleichzeitig verarbeiteten Anfragen pro Mandant:</p>
<ul>
  <li>Schützt Systemressourcen; verhindert, dass ein einzelner Mandant Verbindungen monopolisiert</li>
  <li>Zählerbasiert; schnelle Reaktion</li>
  <li>Gibt 429 zurück oder reiht in Warteschlange ein, wenn überschritten</li>
</ul>
<p class="docs-note">⚠️ <strong>Mandantenbezogen</strong>: Diese Einstellung erfordert eine tenant_id; konfigurieren Sie sie auf der Mandantenverwaltungsseite.</p>`,
  },

  // 同步的扁平键
}