// landing.ts — Textes de la page d'accueil (page d'accueil invité).
//
// 2026-07-05 : Mis à jour avec le contenu réel du projet official-deploy actuel (correspond à LandingView.vue).
export default {
  kicker: 'Noyau Open Source · Localisation Chine · Niveau Entreprise',
  title: 'LLM Gateway — Passerelle IA Open Source pour les Marchés Mondiaux',
  subtitle: 'La seule passerelle IA combinant noyau open source et localisation approfondie en Chine. Gouvernance d\'entreprise, accès LLM mondial, conformité et souveraineté des données — tout en open source.',
  featuresTitle: 'Capacités Principales',
  featuresSubtitle: 'Couvrant les aspects clés de l\'accès aux opérations',
  heroPoints: [
    'Noyau Open Source · Apache 2.0',
    'Localisation Chine · Djbh 2.0',
    'Passerelle IA Entreprise',
    'Gouvernance Vibe Coding',
    'Gestion des Actifs de Session IA',
    'Bouclier de Sécurité des Données',
  ],
  features: {
    smartRouting: {
      title: 'Routage Intelligent & Pool d\'Identifiants',
      description: 'Routage automatique par locataire, modèle et type de tâche ; pool d\'empreintes multi-identifiants + sondage adaptatif, basculement en secondes, taux d\'interdiction quasi nul.',
    },
    safety: {
      title: 'Bouclier de Sécurité des Appels',
      description: 'Détection d\'injection de prompt LLM-as-judge (mode observabilité v1) + planification du masquage des données sensibles, défense de conformité d\'entreprise.',
      badge: 'beta',
    },
    cache: {
      title: 'Alignement du Cache & Réduction des Coûts',
      description: 'Stabilisation du préfixe de prompt + mise en cache sémantique, maximisation du taux de réussite du cache KV, réduction des frais de calcul de tokens.',
    },
    agent: {
      title: 'Passerelle Agent & MCP',
      description: 'Registre d\'agents, protocole A2A, hébergement d\'outils MCP et conversion de protocole — mise à niveau du proxy LLM vers la passerelle d\'orchestration d\'agents.',
      badge: 'Prochainement',
    },
    observability: {
      title: 'Observabilité Full-Chain',
      description: 'Journaux de requêtes, audit des décisions de routage, traçage OTel, export d\'événements SIEM/CEF, prêt pour Djbh 2.0 et GDPR.',
    },
    billing: {
      title: 'Système de Facturation MaaS',
      description: 'Plan + crédits + portefeuille à trois niveaux (abonnement / crédit / recharge), boucle de commercialisation complète pour le libre-service des locataires.',
    },
    multiProtocol: {
      title: 'Compatibilité Multi-Protocoles',
      description: 'OpenAI Chat / Anthropic Messages / Responses trois protocoles entrants unifiés, accès transparent aux modèles open source et commerciaux.',
    },
    multiTenant: {
      title: 'Isolation Multi-Locataires',
      description: 'Sécurité au niveau des lignes PostgreSQL RLS + 43 tours d\'audit L1=0, zéro fuite de données entre locataires, stratégie et quota indépendants par locataire.',
    },
  },
  advantagesTitle: 'Pourquoi Choisir LLM Gateway',
  advantagesSubtitle: 'Pour les entreprises mondiales ayant des besoins commerciaux en Chine',
  advantages: {
    local: {
      title: 'Localisation Profonde en Chine',
      description: 'Interface complète en chinois, accès prioritaire aux LLM open source domestiques, intégration Alipay/WeChat Pay, modèles de conformité Djbh 2.0, infrastructure cloud domestique prête',
    },
    private: {
      title: 'Déploiement Privé',
      description: 'Déploiement entièrement privé, les données restent dans l\'entreprise, forme duale k3s + Docker, zéro dépendance externe',
    },
    antiBan: {
      title: 'Système Anti-Interdiction',
      description: 'Rotation de plus de 50 UA + pool d\'empreintes TLS utls + 11 profils de navigateur + rotation automatique de 5 minutes',
    },
    perf: {
      title: 'Plan de Données Haute Performance Go',
      description: 'Implémentation Go native, image légère de 40 Mo, 200 concurrences P99 < 500 ms, relais stable de streaming SSE',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · Noyau Open Source · Localisation Chine · Déploiement Privé',
  ariaPoints: 'Points Forts Clés',
  roadmap: {
    title: 'Feuille de Route Produit',
    subtitle: 'Du plan de données LLM à la passerelle Agent d\'entreprise, construction continue',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'Centre d\'Actifs API Hub + Hébergement d\'Outils MCP',
      description: 'Enregistrement unifié des points de terminaison LLM, des services MCP et des Agents, découverte et réutilisation en libre-service pour les développeurs.',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'Bouclier de Sécurité GA + Intégration SIEM + SpecBoost',
      description: 'Blocage d\'injection de prompt, masquage des données sensibles, enrichissement intelligent de la description API pour améliorer la précision de Function Calling.',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Registre d\'Agents + Passerelle de Protocole A2A',
      description: 'Délégation de tâches inter-agents et orchestration, accès unifié à OpenClaw et aux Agents métier.',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: 'Solutions Sectorielles GA',
      description: 'Quatre modèles sectoriels pour le service client, les RH, les ventes, la logistique, solutions d\'agents prêtes à l\'emploi.',
    },
  },
}
