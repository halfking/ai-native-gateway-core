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
  kicker: 'Cœur sous licence Apache 2.0',
  brandTitle: 'Passerelle centrale pour organisations IA natives',
  // already says that. It says what the thing does instead.
  title:
    'Une passerelle pour tous les appels aux modèles de votre entreprise, où les voir, les mesurer et les couper',
  subtitle:
    'Les protocoles OpenAI, Anthropic et Responses arrivent dans la même passerelle. Routage par locataire et par modèle, facturation, audit et quotas au même endroit. Elle tourne sur vos machines, donc le trafic ne sort pas.',
  featuresTitle: 'Ce que fait la passerelle',
  featuresSubtitle:
    'Entrée, routage, facturation et audit, dans le même point d\'entrée',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'Cœur sous licence Apache 2.0',
    'Plan de données en Go, image 40 Mo',
    '200 appels simultanés, P99 sous 500 ms',
    'Trois protocoles entrants, un seul point d\'accès',
    'Isolation au niveau ligne avec PostgreSQL RLS',
    'k3s ou Docker, sur votre propre matériel',
  ],
  features: {
    smartRouting: {
      title: 'Routage par locataire et par tâche',
      description:
        'La route est choisie à partir du locataire, du modèle et du type de tâche. Quand un fournisseur tombe, une sonde le détecte et le trafic passe au suivant ; un jeu d\'empreintes de credentials gère la rotation.',
    },
    safety: {
      title: 'Injection de prompts et données sensibles',
      description:
        'LLM-as-judge détecte l\'injection de prompts (en v1 il observe seulement, il ne bloque pas) et les champs sensibles sont masqués selon des règles.',
      badge: 'beta',
    },
    cache: {
      title: 'Cache et coût',
      description:
        'Préfixes stabilisés plus cache sémantique. Un meilleur taux de succès sur KV Cache veut dire moins de tokens facturés.',
    },
    agent: {
      title: 'Entrée des agents et de MCP',
      description:
        'Registre des agents, protocole A2A, hébergement d\'outils MCP et traduction de protocoles. On passe d\'un relais vers un LLM à l\'orchestration d\'agents.',
      badge: 'Bientôt',
    },
    observability: {
      title: 'Chaque appel laisse une trace',
      description:
        'Journaux des appels, audit des décisions de routage, traces OTel, export d\'événements SIEM et CEF. Quand quelque chose casse, on peut remonter le chemin.',
    },
    billing: {
      title: 'Un usage qui colle à la facture',
      description:
        'Forfaits, crédits et un portefeuille à trois poches (abonnement, crédit, recharge). Les locataires consultent leur propre usage au lieu de demander à un administrateur.',
    },
    multiProtocol: {
      title: 'Un point d\'accès, trois protocoles',
      description:
        'OpenAI Chat, Anthropic Messages et Responses sont normalisés à l\'entrée. Le client ne change pas, et la passerelle non plus.',
    },
    multiTenant: {
      title: 'Les locataires ne se mélangent pas',
      description:
        'Sécurité au niveau ligne par PostgreSQL RLS, 43 tours d\'audit sans aucun constat L1, politique et quota propres à chaque locataire.',
    },
  },
  advantagesTitle: 'Pourquoi l\'installer soi-même',
  advantagesSubtitle: 'Les données restent chez vous, la facture reste lisible',
  advantages: {
    openSource: {
      title: 'Cœur ouvert',
      description:
        'Plan de données, plan de contrôle, authentification et ordonnanceur de routage sont entièrement publiés sous Apache 2.0. Vous pouvez les modifier et vous pouvez les auditer.',
    },
    private: {
      title: 'Votre propre matériel',
      description:
        'Installation sur vos machines, sous forme k3s ou Docker, sans dépendre d\'un service externe.',
    },
    antiBan: {
      title: 'Un blocage n\'est pas une panne',
      description:
        'Rotation des credentials et sondes actives : quand un credential est bloqué, le suivant prend le relais.',
    },
    perf: {
      title: 'Un plan de données qui tient',
      description:
        'Go natif, image 40 Mo, P99 sous 500 ms à 200 appels simultanés, relais SSE stable.',
    },
  },
  footer: 'Cœur ouvert sous licence Apache 2.0, exécuté sur vos machines.',
  roadmap: {
    title: 'Où cela mène',
    subtitle: 'D\'un plan de données LLM à une passerelle pour les agents d\'entreprise',
    v31: {
      phase: 'v3.1',
      title: 'Centre d\'actifs API Hub et hébergement d\'outils MCP',
      description:
        'Les points d\'accès LLM, les services MCP et les agents sont enregistrés au même endroit, pour que les développeurs les trouvent et les réutilisent sans demander.',
    },
    v32: {
      phase: 'v3.2',
      title: 'Blocage des injections, intégration SIEM, SpecBoost',
      description:
        'La détection d\'injection de prompts passe de l\'observation au blocage, les données sensibles sont masquées et les descriptions d\'API sont enrichies automatiquement pour améliorer la précision du Function Calling.',
    },
    v40: {
      phase: 'v4.0',
      title: 'Registre d\'agents et passerelle de protocole A2A',
      description:
        'Les agents se délèguent des tâches entre eux, et OpenClaw comme les agents maison entrent par la même porte.',
    },
    v50: {
      phase: 'v5.0',
      title: 'Solutions sectorielles, en disponibilité générale',
      description:
        'Des modèles pour le support, les ressources humaines, la vente et la logistique. On en installe un, et ça marche.',
    },
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'Navigation du produit',
  navDownload: 'Télécharger',
  navSetup: 'Installer et activer',
  navActivate: 'Activer en ligne',
  navLicense: 'État de la licence',
  navAgreement: 'Conditions d\'utilisation',
  navSupport: 'Assistance',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'Se connecter',
  ctaDownload: 'Télécharger le paquet',
  ctaActivate: 'Activer la licence',
  ctaAgreement: 'Voir les conditions',
}
