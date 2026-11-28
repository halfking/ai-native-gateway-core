// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — Textes SettingsView. Namespace : category / list / detail / editor / docs / errors.
// Les noms de champs techniques (JSON / RPM / TPM / WebSocket) restent non traduits.
export default {
  category: {
    all: 'Tous',
    compression: 'Compression',
    rateLimit: 'Limite de débit',
    timeout: 'Délai d\'attente',
    retry: 'Réessai',
    routing: 'Routage',
    session: 'Session',
    security: 'Sécurité',
    outputCompliance: 'Conformité de sortie',
    circuitBreaker: 'Disjoncteur',
    loopGuard: 'Garde anti-boucle',
    general: 'Autre',
  },
 list: {
 count: '{n} paramètres',
 empty: 'Aucun paramètre dans cette catégorie',
 colSetting: 'Paramètre',
 colCurrentValue: 'Valeur actuelle',
 colSource: 'Origine',
 colDanger: 'Risque',
 },
  dangerLevel: {
    note: '🟡 Note',
    warn: '🟠 Avertissement',
    danger: '🔴 Danger',
  },
  detail: {
 tenantScopedTitle: 'Paramètre au niveau du tenant',
 tenantScopedBody: 'Ce paramètre s\'applique à un seul tenant et ne peut pas être défini au niveau du système. Accédez à la page Gestion des tenants pour le configurer pour un tenant spécifique.',
 type: 'Type de valeur',
 currentValue: 'Valeur actuelle',
 defaultValue: 'Valeur par défaut',
 options: 'Valeurs possibles',
 dangerLevel: 'Niveau de risque',
 hotReload: 'Rechargement à chaud',
 noRequiresRestart: 'Non (redémarrage requis)',
 observability: 'Point d\'observation',
 detectField: 'Champ de détection {key}',
 emptyHint: '← Sélectionnez un paramètre à gauche pour voir les détails',
    errors: {
      loadListFailed: 'Le chargement de la liste des paramètres a échoué. Veuillez réessayer.',
      loadDetailFailed: 'Le chargement du détail des paramètres a échoué. Veuillez réessayer.',
      saveFailed: 'L\'enregistrement des paramètres a échoué. Veuillez réessayer.',
      rollbackFailed: 'La restauration de la version précédente n\'a pas pu aboutir. Veuillez réessayer.',
      invalidNumber: 'Veuillez saisir un nombre valide',
    },
  },
  editor: {
 newValue: 'Nouvelle valeur',
 aliasPlaceholder: 'Saisissez un alias puis Entrée',
 aliasHint: 'Enregistré sous forme de chaîne séparée par des virgules ; met à jour immédiatement l\'extraction des alias de session.',
 aliasPreviewTitle: 'Aperçu d\'une requête d\'exemple',
 stringPlaceholder: 'Saisissez une valeur textuelle',
 jsonPlaceholder: 'Saisissez une valeur au format JSON',
 jsonHint: 'Utilisez le format JSON pour les types complexes',
 rollback: 'Restaurer',
    rollbackConfirm: 'Restaurer {key} à sa valeur précédente ?',

    enabledText: 'Activé',
    disabledText: 'Désactivé',
    saving: 'Enregistrement…',
    save: 'Enregistrer',
  },
  compression: {
    enumLabels: {
      off: '0 - Désactivé',
      auto: '1 - Seuil auto',
      on4xx: '2 - Sur 4xx',
      smart: 'Intelligent',
      aggressive: 'Agressif',
    },
    enumDescriptions: {
      off: 'La compression est entièrement désactivée.',
      auto: 'Compresser automatiquement lorsque la longueur des messages dépasse le seuil de la fenêtre de contexte.',
      on4xx: 'Compresser et réessayer en cas d\'erreur 4xx (ex. context_length_exceeded).',
    },
    strategyEnum: {
      naive: 'naive - Compression naïve',
      smart: 'smart - Compression intelligente',
      adaptive: 'adaptive - Compression adaptative',
    },
  },
 gateway: {
 modeEnum: {
 simple: 'simple - Mode simple',
 expert: 'expert - Mode expert',
 },
 recommended: '(Recommandé)',
 modeDescriptions: {
 simple: 'Mode simple : la page d’accueil est une liste d’accès réduite ; les outils experts sont regroupés sous « Avancé ».',
 expert: 'Mode expert : navigation complète avec toutes les pages (réglage actuel par défaut).',
 },
 },
 timezone: {
    hint: 'Le fuseau horaire détermine comment chaque plage horaire est agrégée et affichée (limites de jour, rapports, requêtes de journaux). Par défaut : UTC+8 ; prend effet immédiatement après enregistrement.',
    currentNow: 'Décalage horaire actuel : {offset}',
  },
  search: {
    placeholder: 'Rechercher un paramètre : clé / description / catégorie…',
  },
  basicConfig: {
    toggle: 'Réglages de base',
    hotReload: 'Rechargement à chaud',
    group: {
      identity: 'Identité & apparence',
      storage: 'Stockage',
      schedules: 'Tâches planifiées',
    },
    entry: {
      displayName: 'Nom d’affichage de la passerelle',
      skin: 'Thème par défaut',
      timezone: 'Fuseau horaire système',
      gatewayMode: 'Mode de fonctionnement',
      attachmentUrl: 'URL publique des pièces jointes',
      attachmentDir: 'Répertoire de stockage des pièces jointes',
      rollupHour: 'Heure d’agrégation (UTC)',
    },
    skin: {
      auto: 'Suivre le système',
      light: 'Clair',
      dark: 'Sombre',
    },
    infraTitle: 'Infrastructure (lecture seule)',
    infraHint: 'État d’exécution issu de la configuration d’environnement au démarrage. Toute modification exige un redémarrage du service ; les informations d’identification ne sont jamais affichées sur cette page.',
    infraNotConfigured: 'Non configuré',
    infraNotSet: 'Non défini',
    infra: {
      instance: 'Identifiant d’instance',
      redis: 'Redis (cache)',
      database: 'Base de données',
      cacheDir: 'Répertoire de cache (env)',
      attachmentDir: 'Répertoire des pièces jointes (env)',
    },
  },
  docs: {
    compressionModeTitle: '📖 Mode de compression en détail',
    compressionModeContent: `<p>Le <strong>mode de compression</strong> contrôle la façon dont le système gère les conversations trop longues :</p>
<ul>
  <li><code>0 (off)</code> - Désactiver la compression ; renvoyer une erreur en cas de dépassement de contexte.</li>
  <li><code>1 (auto_threshold)</code> - Mode prédictif ; compresser proactivement lorsque les messages approchent de la fenêtre de contexte.</li>
  <li><code>2 (on_4xx)</code> - Mode réactif ; compresser et réessayer après une erreur 4xx. [Recommandé]</li>
</ul>
<p class="docs-note">💡 <strong>Le mode 2 est recommandé</strong> : ne compresser qu\'en cas de besoin, pour éviter un surcoût de performance inutile.</p>`,
    cacheEnabledTitle: '📖 Cache de session en détail',
    cacheEnabledContent: `<p>Le <strong>cache de session</strong> contrôle l\'activation du cache à trois niveaux L1/L2/L3 :</p>
<ul>
  <li><strong>L1</strong> - Cache mémoire (le plus rapide)</li>
  <li><strong>L2</strong> - Cache Redis (intermédiaire)</li>
  <li><strong>L3</strong> - Cache base de données (le plus lent)</li>
</ul>
<p class="docs-note">⚠️ Lorsqu\'il est désactivé, aucun état de session n\'est sauvegardé ; cela affecte la continuité du contexte.</p>`,
    formatConversionTitle: '📖 Conversion de format en détail',
    formatConversionContent: `<p>La <strong>conversion de format</strong> permet la conversion automatique des formats de requêtes entre protocoles :</p>
<ul>
  <li><strong>Chemin Q2</strong> : format Anthropic → modèles OpenAI</li>
  <li><strong>Chemin Q3</strong> : format OpenAI → modèles Anthropic</li>
</ul>
<p class="docs-note">💡 Les surcharges au niveau fournisseur sont prises en charge ; la conversion peut être désactivée pour des fournisseurs spécifiques.</p>`,
    rateLimitRpmTitle: '📖 Limite de débit RPM en détail',
    rateLimitRpmContent: `<p>La <strong>RPM (Requests Per Minute)</strong> limite le nombre de requêtes par locataire et par minute :</p>
<ul>
  <li>Adaptée au contrôle grossier du trafic</li>
  <li>Utilise un algorithme de fenêtre glissante, à la seconde près</li>
  <li>Renvoie HTTP 429 en cas de dépassement</li>
</ul>
<p class="docs-note">⚠️ <strong>Au niveau locataire</strong> : ce paramètre nécessite un tenant_id ; configurez-le sur la page Gestion des locataires.</p>`,
    rateLimitConcurrentTitle: '📖 Limite de débit de concurrence en détail',
    rateLimitConcurrentContent: `<p>La <strong>limite de débit de concurrence</strong> limite le nombre de requêtes simultanées traitées par locataire :</p>
<ul>
  <li>Protège les ressources système ; empêche un seul locataire de monopoliser les connexions</li>
  <li>Basée sur un compteur ; réponse rapide</li>
  <li>Renvoie 429 ou met en file d\'attente en cas de dépassement</li>
</ul>
<p class="docs-note">⚠️ <strong>Au niveau locataire</strong> : ce paramètre nécessite un tenant_id ; configurez-le sur la page Gestion des locataires.</p>`,
  },

  // 同步的扁平键
}
