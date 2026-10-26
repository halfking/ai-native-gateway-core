// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// auditLog.ts — AuditLogView copy. Namespace: page / filter / table / pagination / detail / actions / errors.
// Technical field names (target_type / before_json / after_json) remain untranslated.
export default {
  page: {
    title: 'Journal d\'audit',
    desc: 'Enregistre les actions de gestion des utilisateurs et d\'authentification. Visible uniquement par les super-administrateurs.',
    totalChip: '{n} entrées',
    refresh: 'Actualiser',
    refreshing: 'Actualisation…',
    emptyTitle: 'Aucun enregistrement d\'audit',
    emptyHint: 'Ajustez les filtres ou élargissez la plage horaire, puis réessayez',
    loading: 'Chargement…',
  },
  filter: {
    actorLabel: 'Acteur',
    actorPlaceholder: 'Correspondance partielle sur le nom d\'utilisateur',
    actionLabel: 'Action',
    actionPlaceholder: 'ex. user.* ou auth.*',
    fromLabel: 'De',
  },
  table: {
    headers: {
      time: 'Heure',
      actor: 'Acteur',
      action: 'Action',
      target: 'Cible',
      details: 'Détails',
    },
  },
  detail: {
    titleWithId: 'Détail d\'audit #{id}',
    close: 'Fermer',
    metaTime: 'Heure',
    metaActor: 'Acteur',
    metaAction: 'Action',
    metaTarget: 'Cible',
    beforeTitle: 'Avant',
    afterTitle: 'Après',
    noExtra: 'Aucun détail supplémentaire',
  },
  actions: {
    'user.create': 'Créer un utilisateur',
    'user.update': 'Mettre à jour un utilisateur',
    'user.delete': 'Supprimer un utilisateur',
    'user.reset_password': 'Réinitialiser le mot de passe',
    'user.change_password': 'Modifier le mot de passe',
    'auth.login': 'Connexion',
    'auth.login_failed': 'Échec de connexion',
    'auth.logout': 'Déconnexion',
    'auth.rate_limited': 'Limite de débit atteinte',
    'authentication.login': 'Connexion',
    'authentication.login_failed': 'Échec de connexion',
    'authentication.logout': 'Déconnexion',
  },

  // 扁平键（供 Vue 组件直接使用）
  loadFailed: 'Échec du chargement',
  dash: '—',
}