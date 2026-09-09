// freeDiscovery.ts — free discovery page copy (fr-FR).
export default {
  page: {
      title: "Découverte de ressources gratuites",
      desc: "Configurez des modèles de fournisseurs → scannez les listes de modèles amont → revoyez → importez en lot dans le pool de ressources gratuites (lié au suivi de quota OmniFree)."
    },
  common: {
      refresh: "Actualiser",
      loading: "Traitement…",
      empty: "Aucune donnée",
      actions: "Actions",
      enabled: "Activé",
      disabled: "Désactivé",
      hideForm: "Replier"
    },
  tabs: {
      templates: "Modèles",
      tasks: "Tâches & revue",
      history: "Historique d'import"
    },
  presets: {
      title: "Préréglages de fournisseurs intégrés",
      create: "Créer",
      exists: "Existant",
      createDone: "Modèle créé depuis le préréglage : {name}",
      scannerPending: "Le scanner pour ce protocole est en cours d'adaptation ; le scan échouera pour l'instant",
      keyless: "keyless (sans clé API)"
    },
  form: {
      show: "+ Modèle personnalisé",
      providerCode: "Provider Code *",
      displayName: "Nom affiché",
      baseUrl: "Base URL *",
      apiType: "Protocole API",
      apiKeyEnv: "Variable d'env. clé API",
      apiKeyEnvHint: "La clé elle-même n'est jamais stockée : utilisez une référence d'env $VAR (ex. $GROQ_API_KEY) ; vide = keyless.",
      tosVerdict: "Verdict ToS",
      submit: "Créer le modèle",
      created: "Modèle créé"
    },
  orbi: {
      show: "Importer un JSON de modèle Orbi",
      hint: "Collez le contenu d'un fichier de modèle Orbi pi-providers (JSON avec clé providers au premier niveau). Plusieurs fournisseurs par fichier possible.",
      import: "Importer",
      invalidJson: "Échec de l'analyse JSON, vérifiez le format",
      done: "Import terminé : {created} créés, {failed} échoués"
    },
  tpl: {
      listTitle: "Modèles ({n})",
      name: "Modèle",
      baseUrl: "Base URL",
      apiType: "Protocole API",
      keyEnv: "Variable d'env. clé",
      tos: "ToS",
      enabled: "Activé",
      createdAt: "Créé le",
      scan: "Scanner",
      delete: "Supprimer",
      deleteConfirm: "Supprimer le modèle « {name} » ? Les tâches et résultats de découverte existants ne sont pas affectés.",
      deleted: "Modèle supprimé : {name}"
    },
  scan: {
      title: "Lancer la découverte",
      pickTemplate: "Choisir un modèle activé…",
      start: "Lancer le scan",
      running: "Scan en cours…",
      done: "Scan terminé : {n} modèles trouvés, veuillez examiner les résultats",
      failed: "Échec du scan"
    },
  task: {
      listTitle: "Tâches de découverte ({n})",
      provider: "Fournisseur",
      status: "Statut",
      trigger: "Déclencheur",
      found: "Trouvés",
      imported: "Importés",
      by: "Par",
      time: "Créé le",
      error: "Erreur",
      review: "Examiner les résultats"
    },
  res: {
      title: "Résultats · tâche {id} · {provider}",
      pending: "À examiner",
      all: "Tous",
      model: "ID du modèle",
      displayName: "Nom affiché",
      freeType: "Type gratuit",
      monthly: "Quota mensuel (tokens)",
      daily: "Quota journalier (tokens)",
      tos: "ToS",
      importStatus: "Statut d'import",
      none: "Aucun résultat pour ce filtre",
      policy: "Stratégie de conflit",
      policySkip: "skip : conserver les entrées existantes",
      policyOverwrite: "overwrite : écraser et réactiver",
      policyMerge: "merge : compléter uniquement les champs vides",
      importSelected: "Importer la sélection ({n})",
      importAllPending: "Importer tous les à examiner",
      importedToast: "Import terminé : {imported} importés, {skipped} ignorés, {conflicted} conflits, {failed} échecs"
    },
  status: {
      taskPending: "En attente",
      running: "En cours",
      success: "Succès",
      failed: "Échec",
      review: "À examiner",
      imported: "Importé",
      skipped: "Ignoré",
      conflict: "Conflit"
    },
  trigger: {
      manual: "Manuel",
      scheduled: "Planifié",
      webhook: "Webhook"
    },
  hist: {
      title: "Historique d'import ({n})",
      desc: "Tâches ayant réellement importé des modèles (nombre importé > 0). Cliquez sur Détail pour voir le devenir de chaque résultat.",
      completed: "Terminé le",
      detail: "Détail",
      none: "Aucun import pour l'instant. Terminez un import en lot dans l'onglet Tâches & revue et il apparaîtra ici.",
      detailTitle: "Détail d'import · tâche {id} · {provider}",
      importedAt: "Importé le"
    },
}
