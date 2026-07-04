// French locale for ClientConfigDialog
export default {
  title: 'Générateur de configuration {tool}',
  close: 'Fermer',
  
  step1: {
    title: '① Sélectionner la clé API (toutes les clés sous le locataire actuel)',
    refresh: 'Actualiser',
    refreshing: 'Actualisation…',
    loading: 'Chargement…',
    empty: {
      title: 'Aucune clé API',
      description: 'Il n\'y a aucune clé API disponible sous le locataire actuel. Les administrateurs peuvent cliquer sur le bouton ci-dessous pour demander une nouvelle clé.',
      applyButton: 'Demander une nouvelle clé',
    },
    selected: 'Sélectionné :',
  },
  
  step2: {
    title: '② Système d\'exploitation',
    pathHint: 'Chemin du fichier de configuration :',
  },
  
  step3: {
    title: '③ Sélectionner la portée du modèle',
    featured: 'Modèles en vedette (configuration de routage featured)',
    all: 'Tous les modèles disponibles (depuis la passerelle)',
    featuredPreview: {
      loading: 'Chargement…',
      empty: 'Aucun modèle en vedette configuré dans le routage',
      manageButton: 'Aller à "Politique de routage" pour configurer →',
      manage: 'Gérer →',
    },
    allModels: {
      selectAll: 'Tout sélectionner',
      deselectAll: 'Effacer',
      selected: 'sélectionné',
      filterHint: '({count} correspondance de recherche)',
      searchPlaceholder: '🔍 Rechercher nom de modèle / fournisseur / famille…',
      loading: 'Chargement…',
      noMatch: 'Aucun modèle correspondant',
    },
  },
  
  footer: {
    generated: '{count} configurations de modèle générées',
    generate: 'Générer la configuration',
    generating: 'Génération…',
    regenerate: 'Régénérer',
  },
  
  results: {
    tabs: {
      file: 'Fichier de configuration',
      script: 'Script de configuration',
      manual: 'Étapes manuelles',
    },
    actions: {
      copyContent: 'Copier le contenu',
      downloadFile: 'Télécharger le fichier',
      downloadScript: 'Télécharger le script',
      scriptHint: 'Le script sauvegarde automatiquement les anciens fichiers de configuration',
    },
  },
  
  applyDialog: {
    title: 'Demander une nouvelle clé API',
    close: 'Fermer',
    applicationCode: 'Code d\'application (application_code)',
    applicationCodePlaceholder: 'par ex. default-app',
    description: 'Description',
    descriptionPlaceholder: 'Facultatif : raison / objectif',
    cancel: 'Annuler',
    submit: 'Soumettre la demande',
    submitting: 'Soumission…',
  },
  
  error: {
    applyFailed: 'La demande a échoué',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor ne prend pas en charge l\'écriture de fichiers — veuillez configurer manuellement dans l\'interface Settings',
}
