// French locale for ClientConfigDialog
export default {
  title: 'Générateur de configuration {tool}',
  close: 'Fermer',

  step1: {
    title: "① Sélectionner une clé API disponible",
    refresh: 'Actualiser',
    refreshing: 'Actualisation…',
    loading: 'Chargement…',
    empty: {
      title: 'Aucune clé API',
      description: 'Il n’y a aucune clé API disponible sous le locataire actuel. Les administrateurs peuvent utiliser le bouton « Demander une nouvelle clé » ci-dessous pour en obtenir une.',
      applyButton: 'Demander une nouvelle clé',
    },
    selected: 'Sélectionné :',
  },

  step2: {
    title: '② Système d\'exploitation',
    pathHint: "Emplacement de la configuration :",
  },

  step3: {
    title: "④ Sélectionner la portée des modèles",
    featured: "Modèles en vedette (routage du locataire de la clé sélectionnée)",
    all: 'Tous les modèles disponibles (depuis la passerelle)',
    featuredPreview: {
      empty: "Aucun modèle en vedette configuré pour le locataire sélectionné",
      manageButton: 'Aller à "Politique de routage" pour configurer →',
      manage: 'Gérer →',
    },
    allModels: {
      selectAll: 'Tout sélectionner',
      deselectAll: 'Effacer',
      selected: 'sélectionné',
      filterHint: '({count} correspondance de recherche)',
      searchPlaceholder: '🔍 Rechercher nom de modèle / fournisseur / famille…',
      noMatch: 'Aucun modèle correspondant',
    },
  },

  retry: "Réessayer",
  otherFamily: "Autres",
  gateway: {
    title: "③ URL de l’API de la passerelle",
    hint: "L’adresse de cette passerelle est utilisée par défaut. Les fichiers et scripts générés contiennent la clé API sélectionnée ; conservez-les en sécurité.",
  },
  provider: {
    label: "Nom du fournisseur",
    hint: "Une entrée de même nom et de même URL est mise à jour. Si l’URL diffère, le script s’arrête et demande un autre nom.",
  },
  notes: {
    cherryStudio: "Saisissez la clé sélectionnée dans les paramètres du client. Ce fichier liste uniquement les paramètres.",
    rooCode: "Configurez OpenAI Compatible dans les paramètres de Roo Code.",
  },

  footer: {
    generated: '{count} configurations de modèle générées',
    generate: 'Générer la configuration',
    generating: 'Génération…',
    regenerate: 'Régénérer',
  },

  results: {
    tabs: {
      yaml: "Extrait YAML",
      file: 'Fichier de configuration',
      script: 'Script de configuration',
      manual: 'Étapes manuelles',
    },
    actions: {
      scriptWarning: "Le script contient votre clé. Téléchargez-le et exécutez-le localement ; son contenu n’est pas affiché ici.",
      mergeHint: "Le script sauvegarde l’ancienne configuration, fusionne {provider} et conserve les autres fournisseurs",
      copyContent: 'Copier le contenu',
      downloadFile: 'Télécharger le fichier',
      downloadScript: 'Télécharger le script',
    },
  },

  applyDialog: {
    title: 'Demander une nouvelle clé API',
    applicationCode: 'Code d\'application (application_code)',
    applicationCodePlaceholder: 'par ex. default-app',
    description: 'Description',
    descriptionPlaceholder: 'Facultatif : raison / objectif',
    cancel: 'Annuler',
    submit: 'Soumettre la demande',
    submitting: 'Soumission…',
  },

  error: {
    invalidGatewayURL: "Saisissez une URL API HTTP(S) valide, sans identifiants, paramètres de requête ni fragment.",
    invalidProviderName: "Le nom du fournisseur doit comporter 1 à 64 lettres (A–Z), chiffres, traits de soulignement ou traits d’union et commencer par une lettre ou un chiffre.",
    featuredLoadFailed: "Impossible de charger les modèles en vedette. Réessayez.",
    catalogLoadFailed: "Impossible de charger le catalogue de modèles. Réessayez.",
    selectModel: "Sélectionnez au moins un modèle. Si aucun modèle en vedette n’est disponible, configurez-les d’abord dans Politique de routage.",
    keyUnavailable: "Impossible de lire la clé sélectionnée. Sélectionnez-la à nouveau.",
    generateFailed: "La génération de la configuration a échoué. Réessayez.",
    copyFailed: "La copie a échoué. Téléchargez le fichier.",
    applyFailed: 'La demande a échoué',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor ne prend pas en charge l\'écriture de fichiers. Veuillez configurer manuellement dans l\'interface Settings',
}
