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
    routing: 'Routage',
    session: 'Session',
    security: 'Sécurité',
    outputCompliance: 'Conformité de sortie',
    circuitBreaker: 'Disjoncteur',
    general: 'Autre',
  },
  dangerLevel: {
    note: '🟡 Note',
    warn: '🟠 Avertissement',
    danger: '🔴 Danger',
  },
  list: {
    total: '{n} paramètres au total',
    loading: 'Chargement…',
    empty: 'Aucun paramètre dans cette catégorie',
    table: {
      setting: 'Paramètre',
      currentValue: 'Actuel',
      source: 'Source',
      danger: 'Danger',
    },
  },
  detail: {
    selectPrompt: '← Sélectionnez un paramètre à gauche pour voir les détails',
    type: 'Type',
    currentValue: 'Valeur actuelle',
    defaultValue: 'Valeur par défaut',
    options: 'Options',
    dangerLevel: 'Niveau de danger',
    hotReload: 'Rechargement à chaud',
    hotReloadYes: 'Oui',
    hotReloadNo: 'Non (redémarrage requis)',
    observability: 'Observabilité',
    tenantWarningTitle: 'Paramètre au niveau locataire',
    tenantWarningBody: 'Ce paramètre s\'applique à un seul locataire et ne peut pas être configuré au niveau du système. Veuillez aller sur la page <strong>Gestion des locataires</strong> pour le configurer pour un locataire spécifique.',
    errors: {
      loadListFailed: 'Échec du chargement',
      loadDetailFailed: 'Échec du chargement des détails',
      saveFailed: 'Échec de l\'enregistrement',
      rollbackFailed: 'Échec du rollback',
      invalidNumber: 'Veuillez saisir un nombre valide',
      confirmRollback: 'Confirmer le rollback de {key} à sa valeur précédente ?',
    },
  },
  editor: {
    newValueLabel: 'Nouvelle valeur',
    enabledText: 'Activé',
    disabledText: 'Désactivé',
    stringPlaceholder: 'Saisir une valeur de chaîne',
    jsonPlaceholder: 'Saisir une valeur JSON',
    jsonHint: 'Utiliser JSON pour les types complexes',
    saving: 'Enregistrement…',
    save: 'Enregistrer',
    rollback: 'Rollback',
  },
  compression: {
    selectLabels: {
      off: '0 - Désactivé',
      auto: '1 - Seuil auto',
      on4xx: '2 - Sur 4xx (recommandé)',
      deltaOnly: 'Delta uniquement (delta_only)',
      smart: 'Intelligent (par défaut)',
      aggressive: 'Agressif (aggressive)',
      headroom: 'Headroom (headroom)',
      headroomAggressive: 'Headroom agressif (headroom_aggressive)',
    },
    enumLabels: {
      off: '0 - Désactivé',
      auto: '1 - Seuil auto',
      on4xx: '2 - Sur 4xx',
      deltaOnly: 'Delta uniquement',
      smart: 'Intelligent',
      aggressive: 'Agressif',
      headroom: 'Headroom',
      headroomAggressive: 'Headroom agressif',
    },
    enumDescriptions: {
      off: 'La compression est entièrement désactivée.',
      auto: 'Compresser automatiquement lorsque la longueur des messages dépasse le seuil de la fenêtre de contexte.',
      on4xx: 'Compresser et réessayer en cas d\'erreur 4xx (ex. context_length_exceeded).',
      deltaOnly: 'Conserver uniquement le delta des messages, pas de compression de session.',
      smart: 'Compression intelligente v4 : découpage d\'outils + fenêtre glissante.',
      aggressive: 'Compression agressive v4 : analyse de tâche + compression proactive.',
      headroom: 'Compression Headroom v7 : compression intelligente de tableaux JSON.',
      headroomAggressive: 'Headroom agressif v7 : compression de tableaux plus agressive + stockage CCR.',
    },
    hint: {
      off: 'La compression est entièrement désactivée.',
      auto: 'Compresser automatiquement lorsque la longueur des messages dépasse le seuil de la fenêtre de contexte.',
      on4xx: 'Compresser et réessayer en cas d\'erreur 4xx (ex. context_length_exceeded).',
      deltaOnly: 'Conserver uniquement le delta des messages, pas de compression de session (adapté aux scénarios sans état).',
      smart: 'Compression intelligente v4 : inclut le découpage des définitions d\'outils et l\'optimisation par fenêtre glissante (par défaut recommandé).',
      aggressive: 'Compression agressive v4 : inclut l\'analyse de tâche et les stratégies de compression proactives (taux de compression élevé).',
      headroom: 'Compression Headroom v7 : compresse intelligemment les tableaux JSON (ex. grandes données structurées) tout en maintenant l\'intégrité sémantique.',
      headroomAggressive: 'Mode Headroom agressif v7 : algorithme de compression de tableaux plus agressif + stockage cache à trois niveaux CCR (taux de compression le plus élevé).',
    },
    strategyEnum: {
      naive: 'naive - Compression naïve',
      smart: 'smart - Compression intelligente',
      adaptive: 'adaptive - Compression adaptative',
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
  outputCompliance: '输出合规',
}
