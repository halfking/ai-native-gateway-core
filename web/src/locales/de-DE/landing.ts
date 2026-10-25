// landing.ts — Landing page copy (guest homepage).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass).
// Rebuilt from the zh-CN / en-US samples. The previous draft was still the
// English marketing text: "Core Open Source · Enterprise-Grade · Private
// Deployment", adjectives standing in for facts, em dashes doing the job of
// punctuation, and every description a tidy "X + Y + Z".
// Rules live in DESIGN.md §7 "Voice" at the repo root.
//
// Fact discipline: this version claims only what the product actually does
// (Apache 2.0 / 40 MB image / 200 concurrent with P99 under 500 ms / RLS
// row-level security / 43 audit rounds with zero L1 findings / k3s and Docker
// / three inbound protocols / 15-day trial). No new capability is implied.
export default {
  // Rendered only in the PublicPageShell footer (brand name + this). It is no
  // longer used as a small label above the heading (craft-floor bans kickers).
  kicker: 'Apache-2.0-Kern',
  brandTitle: 'AI-Natives Kern-Gateway für Organisationen',
  // already says that. It says what the thing does instead.
  title:
    'Ein Gateway für alle Modellaufrufe Ihrer Organisation: sichtbar, abgerechnet und abschaltbar',
  subtitle:
    'OpenAI-, Anthropic- und Responses-Protokolle laufen in dasselbe Gateway. Routing nach Mandant und Modell, Abrechnung, Audit und Ratenbegrenzung an einem Ort. Es läuft auf Ihren eigenen Maschinen, der Traffic bleibt im Haus.',
  featuresTitle: 'Was das Gateway kann',
  featuresSubtitle: 'Annahme, Routing, Abrechnung und Audit in demselben Zugang',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'Kern unter Apache 2.0',
    'Datenebene in Go, Image 40 MB',
    '200 gleichzeitige Anfragen, P99 unter 500 ms',
    'Drei Eingangsprotokolle, ein Endpunkt',
    'Zeilengenaue Trennung mit PostgreSQL RLS',
    'k3s oder Docker, auf eigener Hardware',
  ],
  features: {
    smartRouting: {
      title: 'Routing nach Mandant und Aufgabe',
      description:
        'Die Route wird aus Mandant, Modell und Aufgabentyp gewählt. Wenn ein Anbieter ausfällt, erkennt das eine Prüfung und der Verkehr wandert zum nächsten; ein Pool aus Credential-Fingerprints übernimmt die Rotation.',
    },
    safety: {
      title: 'Prompt-Injection und sensible Daten',
      description:
        'LLM-as-judge erkennt Prompt-Injection (v1 beobachtet nur und blockiert nicht), sensible Felder werden regelbasiert maskiert.',
      badge: 'beta',
    },
    cache: {
      title: 'Cache und Kosten',
      description:
        'Stabilisierte Prefixe plus semantischer Cache. Eine höhere KV-Cache-Trefferquote bedeutet weniger berechnete Token.',
    },
    agent: {
      title: 'Agent- und MCP-Zugang',
      description:
        'Agent-Registry, A2A-Protokoll, MCP-Tool-Hosting und Protokollübersetzung. Aus einem LLM-Proxy wird eine Agent-Orchestrierung.',
      badge: 'Demnächst',
    },
    observability: {
      title: 'Jede Anfrage hinterlässt eine Spur',
      description:
        'Anfragelogs, Audit der Routing-Entscheidungen, OTel-Traces, SIEM- und CEF-Export. Wenn etwas klemmt, lässt sich der Weg zurückverfolgen.',
    },
    billing: {
      title: 'Verbrauch passt zur Rechnung',
      description:
        'Tarife, Guthaben und eine Wallet aus drei Pools (Abo, Kredit, Aufladung). Mandanten prüfen ihren eigenen Verbrauch, statt einen Admin zu fragen.',
    },
    multiProtocol: {
      title: 'Ein Endpunkt, drei Protokolle',
      description:
        'OpenAI Chat, Anthropic Messages und Responses werden beim Eingang vereinheitlicht. Der Client bleibt unverändert, das Gateway auch.',
    },
    multiTenant: {
      title: 'Mandanten bleiben getrennt',
      description:
        'PostgreSQL RLS auf Zeilenebene, 43 Auditrunden ohne einen einzigen L1-Befund, je Mandant eigene Regeln und Kontingente.',
    },
  },
  advantagesTitle: 'Warum selbst betreiben',
  advantagesSubtitle: 'Die Daten bleiben bei Ihnen, die Rechnung bleibt lesbar',
  advantages: {
    openSource: {
      title: 'Offener Kern',
      description:
        'Datenebene, Kontrollebene, Authentifizierung und Routing-Planer sind vollständig unter Apache 2.0 offen. Sie können es ändern und Sie können es prüfen.',
    },
    private: {
      title: 'Eigene Hardware',
      description:
        'Installation auf Ihren Maschinen, als k3s oder Docker, ohne Abhängigkeit von einem externen Dienst.',
    },
    antiBan: {
      title: 'Eine Sperre ist kein Ausfall',
      description:
        'Credential-Rotation plus aktive Prüfungen: Ist eine Zugangsberechtigung gesperrt, übernimmt die nächste.',
    },
    perf: {
      title: 'Eine Datenebene, die das aushält',
      description:
        'Natives Go, Image 40 MB, bei 200 gleichzeitigen Anfragen bleibt P99 unter 500 ms, die SSE-Weiterleitung bleibt stabil.',
    },
  },
  footer: 'Offener Kern unter Apache 2.0, läuft auf Ihren eigenen Maschinen.',
  roadmap: {
    title: 'Wohin das führt',
    subtitle: 'Von der LLM-Datenebene zum Gateway für Unternehmens-Agenten',
    v31: {
      phase: 'v3.1',
      title: 'API-Hub-Assetcenter und MCP-Tool-Hosting',
      description:
        'LLM-Endpunkte, MCP-Dienste und Agenten werden an einer Stelle registriert, damit Entwickler sie selbst finden und weiterverwenden.',
    },
    v32: {
      phase: 'v3.2',
      title: 'Injektionsblocking, SIEM-Anbindung, SpecBoost',
      description:
        'Die Erkennung von Prompt-Injection geht vom Beobachten zum Blockieren, sensible Daten werden maskiert und API-Beschreibungen automatisch ergänzt, damit Function Calling genauer trifft.',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent-Registry und A2A-Protokoll-Gateway',
      description:
        'Agenten delegieren Aufgaben einander, OpenClaw und eigene Agenten kommen durch dieselbe Tür.',
    },
    v50: {
      phase: 'v5.0',
      title: 'Branchenlösungen, allgemein verfügbar',
      description: 'Vorlagen für Support, Personal, Vertrieb und Logistik. Installieren und es läuft.',
    },
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'Produktnavigation',
  navDownload: 'Download',
  navSetup: 'Installieren und aktivieren',
  navActivate: 'Online aktivieren',
  navLicense: 'Lizenzstatus',
  navAgreement: 'Nutzungsbedingungen',
  navSupport: 'Support',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'Anmelden',
  ctaDownload: 'Paket herunterladen',
  ctaActivate: 'Lizenz aktivieren',
  ctaAgreement: 'Bedingungen ansehen',
}
