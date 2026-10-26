export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'Le chargement des données de VibeCoding a échoué. Veuillez réessayer.',
    fillRequired: 'Veuillez remplir les champs obligatoires',
    projects: 'Projets',
    sessions: 'Sessions',
    codeReviews: 'Revues de code',
    createProject: 'Créer un projet',
    createProjectTitle: 'Créer un nouveau projet',
    createProjectSuccess: 'Projet créé avec succès',
    createProjectFailed: 'La création du projet a échoué. Veuillez réessayer.',
    newSession: 'Nouvelle session',
    createSessionTitle: 'Créer une nouvelle session',
    createSessionSuccess: 'Session créée avec succès',
    createSessionFailed: 'La création de la session a échoué. Veuillez réessayer.',
    viewSessions: 'Voir les sessions',
    viewReviews: 'Voir les revues',
    showAll: 'Tout afficher',
    reviewDetail: 'Détail de la revue',
    /**
     * États vides (2026-10-27 · incrément 14 — « état vide en 5 points »).
     * ★ Les trois tableaux partageaient une seule phrase `hyper.list.empty`
     *   (« La liste ne contient aucun enregistrement pour l’instant »), alors
     *   qu’ils sont **deux** états différents :
     *   · projects — sur le tableau **brut** ⇒ vide = aucun projet n’a encore
     *     été créé (FirstUse)
     *   · sessions / reviews — sur le tableau **filtré** ⇒ vide = filtré
     *     (Filtered)
     *   ⇒ Une seule phrase pour deux états, c’est « vous n’avez pas encore
     *   commencé » et « vos filtres sont trop étroits » dits de la même façon.
     */
    emptyFirstUseProjects: 'Aucun projet n’a encore été créé.',
    emptyFilteredSessions: 'Aucun enregistrement de session ne correspond aux filtres actuels.',
    emptyFilteredReviews: 'Aucun enregistrement de revue de code ne correspond aux filtres actuels.',
    projectName: 'Nom du projet',
    projectNamePlaceholder: 'Saisissez le nom du projet',
    language: 'Langage',
    languagePlaceholder: 'ex. TypeScript',
    framework: 'Framework',
    frameworkPlaceholder: 'ex. Vue 3',
    projectId: 'ID du projet',
    sessionName: 'Nom de la session',
    taskType: 'Type de tâche',
    taskTypePlaceholder: 'ex. revue de code, refactorisation, explication',
    startedAt: 'Démarré le',
    endedAt: 'Terminé le',
    filePath: 'Chemin du fichier',
    score: 'Note',
    summary: 'Résumé',
    issues: 'Problèmes',
    suggestions: 'Recommandations',
    category: 'Catégorie',
    reviewedAt: 'Revu le',
    line: 'Ligne',
    severity: 'Gravité',
    message: 'Message',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: 'Seuls les super administrateurs peuvent maintenir les nœuds ; affichage en lecture seule.',
    nodeDetailDefaultTenantOnlyHint: 'Seul le tenant default peut maintenir les nœuds ; affichage en lecture seule.',

















    pingFailedWithReason: 'Échec du ping de session : {reason}. Veuillez réessayer.',
    saveSettingFailedWithReason: 'Le paramètre n’a pas pu être enregistré : {reason}. Veuillez réessayer.',
    maintainFailed: 'La maintenance n’a pas pu aboutir. Veuillez réessayer.',
    modelMaintainFailed: 'La maintenance des modèles n’a pas pu aboutir. Veuillez réessayer.',
    fpSlotLoadFailed: 'Le chargement des slots d’empreinte digitale a échoué. Veuillez réessayer.',
    fpSlotProviderMissing: 'provider_id est manquant, les slots d’empreinte digitale n’ont pas pu être chargés. Sélectionnez d’abord un fournisseur, puis réessayez.',
    accessErrorsHint: 'Appels en échec + erreurs de routage · Ouvrir le détail de la requête d’origine',
    recentRequestsHint: 'Dernière heure{source} · Voir le détail des requêtes',
  }
,

}
