// modulesView.ts — Page de gestion des modules.
export default {
  pageTitle: 'Modules',
  pageSubtitle: 'Gestion centralisée des modules de fonctionnalités entreprise. Activez ou désactivez les capacités selon vos besoins.',
  modulesEnabled: 'modules activés',
  loading: 'Chargement…',

  category: {
    compression: 'Compression des requêtes',
    session: 'Gestion des sessions',
    security: 'Sécurité',
    rate_limit: 'Limitation de débit',
    general: 'Général',
    integration: 'Intégration',
  },

  status: {
    enabled: 'Activé',
    disabled: 'Désactivé',
    processing: 'Traitement…',
    enabledAction: 'Désactiver ce module',
    disabledAction: 'Activer ce module',
  },

  dangerLevel: {
    safe: 'Sûr',
    warn: 'Attention',
    danger: 'Dangereux',
    breaking: 'Critique',
    unknown: 'Inconnu',
  },

  tabs: {
    overview: 'Aperçu',
    config: 'Configuration',
    integration: 'Intégration',
    status: 'Exécution',
  },

  overview: {
    sectionDescription: 'Description',
    sectionCapabilities: 'Capacités',
    labelKey: 'Clé du module',
    labelDanger: 'Niveau de danger',
    labelConfigCount: 'Éléments de configuration',
    labelStatus: 'État actuel',
    viewAllSettings: 'Voir tous les paramètres système',
    testConnection: 'Tester la connexion',
    testSuccess: 'Test de connexion réussi',
    testFailed: 'Échec du test de connexion',
    testInProgress: 'Envoi du message de test…',
    dependenciesTitle: 'Modules dépendants',
    required: 'Obligatoire',
    optional: 'Facultatif',
    enableAllDeps: 'Activer {count} dépendances obligatoires manquantes',
    enableAllDepsHint: 'Le backend activera toutes les dépendances obligatoires manquantes dans un ordre stable, puis activera ce module.',
    cascading: 'Activation des dépendances…',
    cascadeSuccess: 'Activation automatique de {count} dépendances : {names}',
    cascadeEmpty: 'Toutes les dépendances étaient déjà activées ; le module est maintenant actif.',
    cascadeAlreadyEnabled: 'Le module est déjà activé, aucune activation en cascade nécessaire.',
    cascadeLogTitle: 'Dépendances activées en cascade lors de cette exécution :',

    sectionDependencies: 'Dependencies',

    dependencyDisabled: 'Dependency module is disabled',

    notEnabled: 'Not enabled',
  },

  config: {
    noSettings: 'Ce module n\'a pas de paramètres configurables.',
    sourceDefault: 'Défaut',
    sourceEnv: 'Variable d\'environnement',
    sourceDb: 'Base de données',
    switchOn: 'Activé',
    switchOff: 'Désactivé',
    inputPlaceholder: 'Saisir {description}',
    sections: {
      connection: 'Connexion',
      alerts: 'Transfert d\'alertes',
      approvals: 'Notifications d\'approbation',
      commands: 'Panneau de commandes',
      security: 'Sécurité',
      general: 'Général',
    },
  },

  feishu: {
    connectionHint: 'Créez un bot personnalisé sur la plateforme ouverte Feishu, puis collez l\'URL du Webhook ci-dessous. Consultez la documentation officielle Feishu pour plus de détails.',
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'Interrupteur principal et mode de déclenchement : ils décident si la reprise de session est active et quand elle part',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 conditions de déclenchement sont évaluées en parallèle : une seule suffit à déclencher la reprise, et la plus stricte l\'emporte.',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Moteur de résumé et modèle de prompt : comment compresser l\'ancienne session en un contexte lisible par la nouvelle',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Délai de repos, plafond par session, nouvelle tentative en cas d\'échec et notifications par log ou webhook',
  },

  integration: {
    docsLabel: 'Documentation : ',
    stepsTitle: 'Étapes de configuration',
    enabledStatus: 'Intégration activée',
    disabledHint: 'Intégration désactivée: veuillez d\'abord activer le module',
    feishuSteps: [
      'Créer un bot personnalisé sur la plateforme ouverte Feishu',
      'Copier l\'URL du webhook et la coller dans la configuration ci-dessous',
      '(Facultatif) Configurer le jeton de vérification de signature et la clé de chiffrement',
      'Cliquer sur « Tester la connexion » après configuration',
      'Activer le commutateur « Intégration du bot Feishu »',
    ],
    feishuBotIntegration: 'Intégration du bot Feishu',
    wechatSteps: [
      "Créez une application dans la console d'administration WeCom",
      "(Facultatif) Configurez l'URL de callback et le Token dans \"Recevoir des messages\"",
      'Copiez CorpID, AgentID et Secret dans la configuration',
      "(Facultatif) Configurez l'URL du webhook du bot de groupe",
      '(Facultatif) Configurez EncodingAESKey pour le chiffrement des callbacks',
      'Activez l\'interrupteur "Intégration du bot WeChat"',
      'Assurez-vous que tous les modules prérequis sont activés',
    ],
    wechatBotIntegration: 'Intégration du bot WeChat',
    dingtalkSteps: [
      'Ajoutez un « Robot personnalisé » dans un groupe DingTalk, choisissez la sécurité « Signature » et copiez le Webhook',
      'Collez l’URL Webhook ci-dessous et renseignez le Secret de signature',
      '(Optionnel) Créez une application interne dans la console et renseignez AppKey/AppSecret/AgentID pour activer la notification de travail',
      'Configurez l’URL de rappel d’approbation (/api/webhooks/dingtalk/approval-callback) comme récepteur du robot',
      'Activez le commutateur « Intégration du bot DingTalk »',
      'Assurez-vous que les modules requis (compression, détection d’injection, cache de session, audit de session) sont activés',
    ],
    dingtalkBotIntegration: 'Intégration du bot DingTalk',
  },

  empty: {
    selectModule: 'Sélectionnez un module pour afficher les détails et la configuration',
  },


  error: {
    loadFailed: 'Échec du chargement de la liste des modules',
    operationFailed: 'Opération échouée',
    saveFailed: 'Échec de l\'enregistrement de la configuration',
    testFailed: 'Échec du test',
  },

  // 同步的扁平键

  modeBannerTitle: 'This page only does three things:',

  modeBannerBody: 'Activez ou désactivez les modules installés et téléchargez ceux qui sont déjà licenciés mais pas activés. Ni prix, ni commande, ni abonnement ici.',
}