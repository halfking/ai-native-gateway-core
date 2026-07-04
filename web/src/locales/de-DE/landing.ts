// landing.ts — Landing-Page-Texte (Gast-Startseite).
//
// 2026-07-05: Aktualisiert auf den tatsächlichen Inhalt des aktuellen official-deploy-Projekts (entspricht LandingView.vue).
export default {
  kicker: 'Kern Open Source · China-Lokalisierung · Enterprise-Grade',
  title: 'LLM Gateway — Open Source AI Gateway für globale Märkte',
  subtitle: 'Das einzige AI Gateway, das Kern-Open-Source mit tiefer China-Lokalisierung kombiniert. Enterprise-Governance, globaler LLM-Zugang, Compliance und Datensouveränität — alles Kern-Open-Source.',
  featuresTitle: 'Kernfunktionen',
  featuresSubtitle: 'Abdeckung wichtiger Aspekte vom Zugang bis zum Betrieb',
  heroPoints: [
    'Kern Open Source · Apache 2.0',
    'China-Lokalisierung · Djbh 2.0',
    'Enterprise AI Gateway',
    'Vibe Coding Governance',
    'AI Session Asset Management',
    'Datensicherheitsschild',
  ],
  features: {
    smartRouting: {
      title: 'Smart Routing & Credential Pool',
      description: 'Auto-Routing nach Mandant, Modell und Aufgabentyp; Multi-Credential-Fingerprint-Pool + adaptives Probing, Failover in Sekunden, nahezu null Ban-Rate.',
    },
    safety: {
      title: 'Call Security Shield',
      description: 'LLM-as-judge Prompt-Injection-Erkennung (v1 Observability-Modus) + Planung zur Maskierung sensibler Daten, Enterprise-Compliance-Verteidigung.',
      badge: 'beta',
    },
    cache: {
      title: 'Cache-Ausrichtung & Kostensenkung',
      description: 'Prompt-Präfix-Stabilisierung + semantisches Caching, Maximierung der KV Cache-Trefferquote, Reduzierung des Token-Rechenaufwands.',
    },
    agent: {
      title: 'Agent & MCP Gateway',
      description: 'Agent-Registry, A2A-Protokoll, MCP-Tool-Hosting und Protokollkonvertierung — Upgrade vom LLM-Proxy zum Agent-Orchestrierungs-Gateway.',
      badge: 'Demnächst',
    },
    observability: {
      title: 'Full-Chain Observability',
      description: 'Request-Logs, Routing-Entscheidungs-Audit, OTel-Tracing, SIEM/CEF-Event-Export, Djbh 2.0 und GDPR bereit.',
    },
    billing: {
      title: 'MaaS-Abrechnungssystem',
      description: 'Plan + Credits + Drei-Pool-Wallet (Abonnement / Kredit / Aufladung), vollständige Kommerzialisierungsschleife für Mandanten-Self-Service.',
    },
    multiProtocol: {
      title: 'Multi-Protokoll-Kompatibilität',
      description: 'OpenAI Chat / Anthropic Messages / Responses drei eingehende Protokolle vereinheitlicht, nahtloser Zugang zu Open-Source- und kommerziellen Modellen.',
    },
    multiTenant: {
      title: 'Multi-Mandanten-Isolation',
      description: 'PostgreSQL RLS Row-Level-Security + 43 Audit-Runden L1=0, null Datenleck zwischen Mandanten, unabhängige Richtlinie und Quote pro Mandant.',
    },
  },
  advantagesTitle: 'Warum LLM Gateway wählen',
  advantagesSubtitle: 'Für globale Unternehmen mit China-Geschäftsanforderungen',
  advantages: {
    local: {
      title: 'Tiefe China-Lokalisierung',
      description: 'Vollständige chinesische Benutzeroberfläche, inländischer Open-Source-LLM-Prioritätszugang, Alipay/WeChat-Pay-Integration, Djbh 2.0-Compliance-Vorlagen, inländische Cloud-Infrastruktur bereit',
    },
    private: {
      title: 'Private Bereitstellung',
      description: 'Vollständig private Bereitstellung, Daten bleiben im Unternehmen, k3s + Docker Dualform, null externe Abhängigkeiten',
    },
    antiBan: {
      title: 'Anti-Ban-System',
      description: '50+ UA-Rotation + utls TLS-Fingerprint-Pool + 11 Browser-Profile + 5-Minuten-Auto-Rotation',
    },
    perf: {
      title: 'Go High-Performance Data Plane',
      description: 'Native Go-Implementierung, 40MB leichtes Image, 200 Parallelität P99 < 500ms, SSE-Streaming stabiles Relay',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · Kern Open Source · China-Lokalisierung · Private Bereitstellung',
  ariaPoints: 'Wichtige Highlights',
  roadmap: {
    title: 'Produkt-Roadmap',
    subtitle: 'Von der LLM-Datenebene zum Enterprise Agent Gateway, kontinuierlicher Aufbau',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub Asset Center + MCP-Tool-Hosting',
      description: 'Einheitliche Registrierung von LLM-Endpunkten, MCP-Services und Agents, Entwickler-Self-Service-Erkennung und Wiederverwendung.',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'Security Shield GA + SIEM-Integration + SpecBoost',
      description: 'Prompt-Injection-Blockierung, Maskierung sensibler Daten, intelligente API-Beschreibungsanreicherung zur Verbesserung der Function-Calling-Genauigkeit.',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent Registry + A2A Protocol Gateway',
      description: 'Cross-Agent-Task-Delegation und Orchestrierung, einheitlicher Zugang zu OpenClaw und Business Agents.',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: 'Branchenlösungs-GA',
      description: 'Vier Branchenvorlagen für Kundenservice, HR, Vertrieb, Logistik, sofort einsatzbereite Agent-Lösungen.',
    },
  },
}
