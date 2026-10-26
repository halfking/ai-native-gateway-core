// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// credentialMonitor.ts — Page de surveillance des identifiants (CredentialMonitorView).
// Reflète la structure de zh-CN/credentialMonitor.ts. Les valeurs d'énumération du backend
// (ready/degraded/healthy/warning/broken/etc.) restent telles quelles dans l'UI —
// les traduire romprait le contrat de l'API.
export default {
  page: {
    title: 'Surveillance des identifiants',
  },
  table: {
    cell: {
      modelsNotMeasured: 'Cet identifiant n’a renvoyé aucun décompte de modèles cette fois (non mesuré). Ce n’est pas « 0 disponible »',
    },
  },
  drawer: {
    tab: {
      overview: 'Aperçu',
      models: 'Modèles',
      requests: 'Requêtes',
    },
    operationFailedWithReason: 'Échec de l’opération : {reason}. Veuillez réessayer.',
    offlineFailedWithReason: 'Échec de la mise hors ligne : {reason}. Veuillez réessayer.',
    onlineFailedWithReason: 'Échec de la mise en ligne : {reason}. Veuillez réessayer.',
    degradeFailedWithReason: 'Échec de la dégradation : {reason}. Veuillez réessayer.',
    upgradeFailedWithReason: 'Échec de la mise à niveau : {reason}. Veuillez réessayer.',
    settingFailedWithReason: 'Échec de la configuration : {reason}. Veuillez réessayer.',
    manualDisabledTooltip: 'Désactivé manuellement, l’auto-probe ne le touchera pas. Vous pouvez l’activer manuellement.',
    availableTooltip: 'Actuellement disponible, vous pouvez le désactiver manuellement.',
    autoControlledTooltip: 'Contrôlé par l’auto-probe (broken_confirmed, etc.). Vous pouvez le désactiver manuellement (mise hors ligne forcée).',
    rowSelectsMonitor: 'Sélectionnez une ligne pour afficher la supervision à droite.',
    selectModelRow: 'Sélectionnez une ligne de modèle à gauche pour l’afficher.',
  },
  chart: {
    allHealthy: 'Tous en bonne santé',
    errorsWhenHealthy: 'Distribution des erreurs: tous en bonne santé',
    errorsTitle: 'Distribution des erreurs',
    /**
     * État vide (2026-10-27 · incrément 14 — « état vide en 5 points »).
     * ★ Ici l’état est un **Filtered** : la plage de temps et les filtres
     *   ne laissent rien passer. ⇒ nommer ce qui manque (des identifiants) et
     *   donner la prochaine action utile (élargir / assouplir), au lieu de
     *   laisser l’utilisateur deviner pourquoi la carte est vide.
     */
    emptyFiltered: 'Aucun enregistrement d’identifiant ne correspond à la plage de temps ni aux filtres actuels ; élargissez la plage de temps ou assouplissez les filtres, puis consultez à nouveau.',
  },
  error: {
    selectFirst: 'Veuillez d\'abord sélectionner des identifiants',
    clearFailed: 'Échec de l\'effacement : ',
    batchFailed: 'Échec de l\'opération par lot : ',
    superAdminOnlyHint: 'Seuls les super administrateurs peuvent effectuer les corrections d\'état ; actuellement en lecture seule.',
    superAdminOnlyError: 'Seuls les super administrateurs peuvent effectuer les corrections d\'état.',
  },
}
