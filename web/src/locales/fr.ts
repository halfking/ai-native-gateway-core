export default {
  // Général
  login: 'Connexion',
  logout: 'Déconnexion',
  changePassword: 'Changer le mot de passe',
  cancel: 'Annuler',
  confirm: 'Confirmer',
  save: 'Enregistrer',
  delete: 'Supprimer',
  edit: 'Modifier',
  search: 'Rechercher',
  reset: 'Réinitialiser',
  submit: 'Soumettre',
  back: 'Retour',
  next: 'Suivant',
  previous: 'Précédent',
  close: 'Fermer',
  
  // Rôles utilisateur
  role: {
    super_admin: 'Super Administrateur',
    tenant_admin: 'Administrateur Locataire',
  },
  
  // Navigation
  nav: {
    collapseSidebar: 'Réduire le menu',
    expandSidebar: 'Déployer la barre latérale',
  },
  
  // Mot de passe
  password: {
    changeSuccess: 'Mot de passe modifié avec succès',
    changeFailed: 'Échec de la modification du mot de passe',
  },
  
  // Informations de version
  version: 'Version',
  build: 'Build',

  // 2026-07-02 (affichage des pièces jointes request-logs) : textes relatifs
  // aux pièces jointes, conformément au document de référence §6.
  requests: {
    list: {
      table: {
        attachmentsTitle: 'Pièces jointes',
        noAttachments: 'Aucune pièce jointe',
      },
    },
    detail_extra: {
      attachmentsTab: 'Pièces jointes',
      attachmentsLoading: 'Chargement des pièces jointes…',
      noAttachments: 'Aucune pièce jointe',
      clickToPreviewTitle: 'Cliquer pour agrandir',
      download: 'Télécharger',
      downloadOriginal: 'Télécharger l\'original',
      closePreview: 'Fermer',
    },
  },

  // 2026-07-03 (flux de requêtes en temps réel) : swim lane du tableau de bord.
  dashboard: {
    liveStream: {
      title: 'Flux de requêtes en temps réel',
      connected: 'Connecté',
      connecting: 'Connexion…',
      reconnecting: 'Reconnexion…',
      disconnected: 'Déconnecté',
      unsupported: 'Non supporté',
      pause: 'Pause',
      resume: 'Reprendre',
      filterAll: 'Tous les statuts',
      filterSuccess: 'Succès uniquement',
      filterInProgress: 'En cours',
      filterGroupFailures: 'Détail des échecs',
      filterFailure5xx: 'Serveur / amont (5xx)',
      filterFailure4xx: 'Client / auth (4xx)',
      filterFailureTimeout: 'Délai / réseau',
      filterFailureNotFound: 'Routage / modèle introuvable',
      filterFailureOther: 'Autres échecs',
      empty: 'En attente de requêtes…',
      countTooltip: '{buffer} en mémoire / {visible} visibles',
      countAria: '{buffer} requêtes en mémoire, {visible} visibles',
      legend: {
        model: 'Famille de modèles',
        status: 'Statut',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: 'Domestique',
        oss: 'Open source',
        other: 'Autre',
        success: 'Succès',
        inProgress: 'En cours',
        failure: 'Échec',
      },
    },
  },
}