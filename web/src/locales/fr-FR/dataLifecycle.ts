export default {
  refresh: "Actualiser",
  loading: "Chargement…",
  storage: {
    migrateFailedWithReason: 'La migration n’a pas pu aboutir : {reason}. Veuillez réessayer.',
  },
  /**
   * Textes du gestionnaire des champs volumineux (BlobManager).
   * ★ État vide (2026-10-27 · incrément 14 — « état vide en 5 points ») :
   *   il utilisait la clé générique `common.feedback.noData`
   *   (« Aucun contenu à afficher pour l’instant »).
   *   Cette page **n’a pas de filtre de temps** ⇒ pas de plage de temps
   *   (on n’invente pas un fait que la page ignore).
   */
  blob: {
    emptyNoResult: 'Aucun enregistrement de champ volumineux n’a encore été créé.',
  },
  storageOverview: {
    summary: {
      databaseTitle: "Base PostgreSQL",
      databaseSubtitle: "Empreinte totale (tables + index + TOAST)",
      databaseMetric: "Base de données",
      tablesMetric: "Tables",
      indexesMetric: "Indexs",
      toastMetric: "TOAST",
      diskTitle: "Disque local",
      diskUsedOf: "Utilisé / {total}",
      path: "Chemin",
      used: "Utilisé",
      free: "Libre",
      columnarTitle: "Stockage colonnaire (citus_columnar)",
      enabled: "Activé",
      notInstalled: "Non installé",
      columnarTableCount: "Nombre de tables (partitions incluses)",
      columnarTableCountMeta: "Tables utilisant la méthode d’accès columnar",
      columnarTotalColumns: "Nombre total de colonnes",
      columnarTotalColumnsMeta: "Somme des colonnes sur toutes les tables colonnaires",
      columnarSize: "Taille colonnaire",
      columnarSizeMeta: "Taille compressée (métadonnées de chunk incluses)",
      columnarDbShare: "Part DB",
      columnarDbShareMeta: "Colonnaire / taille totale de la base",
      columnarNotInstalledFallback: "L’extension citus_columnar n’est pas installée",
      localLogsTitle: "Répertoire local des journaux",
      notFound: "Introuvable",
      fileCount: "Fichiers",
      size: "Taille",
      oldest: "Plus ancien",
      newest: "Plus récent",
      localLogsMissingHint: "Répertoire de logs introuvable ; la rotation / sauvegarde gzip n’écrira pas ici.",
      warningsTitle: "Avertissements"
    },
    tablesTitle: "Top {n} tables de la base",
    refresh: "Actualiser",
    refreshLoading: "Chargement…",
    columns: {
      tableName: "Table",
      rows: "Lignes",
      totalSize: "Taille totale",
      indexes: "Index",
      toast: "TOAST",
      percent: "Part",
      partition: "Partition",
      actions: "Actions"
    },
    partitionBadge: "Partition",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table}: sans verrou, récupère les tuples morts",
      vacuumFullTitle: "VACUUM FULL {table}: avec verrou, rend l'espace au SE",
      reindexTitle: "REINDEX TABLE {table}: avec verrou, reconstruit les index"
    },
    modal: {
      vacuumTitle: "🧹 Confirmer VACUUM",
      vacuumFullTitle: "🗜 Confirmer VACUUM FULL",
      reindexTitle: "🔧 Confirmer REINDEX",
      intro: "Exécution de {op} sur {table}.",
      currentSize: "Taille actuelle : ",
      rowCount: "Nombre de lignes : ",
      partition: "Partitionnée : ",
      partitionYes: "Oui (⚠️ le verrou touche toutes les partitions filles)",
      partitionNo: "Non",
      flow: "Déroulement",
      impact: "Impact",
      disk: "Effet sur l'espace disque",
      time: "Durée estimée",
      cancel: "Annuler",
      confirm: "Exécuter {op}",
      success: "{op} {table} terminé: {size} libérés ({pct}%)",
      failed: "{op} échoué : {msg}"
    },
    rowResult: {
      running: "{op} en cours…",
      success: "{op} : {size} libérés ({pct}%, {ms}ms)",
      failed: "{op} échoué : {msg}"
    },
    op: {
      vacuum: {
        flow: "Parcourt la table à la recherche de tuples morts et marque leur espace comme réutilisable (pas encore rendu au système d'exploitation). Met aussi à jour les statistiques pour aider le planificateur.",
        impact: "Ne verrouille pas la table ; SELECT / INSERT / UPDATE / DELETE continuent normalement. Consomme un peu plus de CPU et d'IO pendant l'exécution.",
        disk: "Ne rend pas immédiatement l'espace au système d'exploitation ; il est réutilisé dans la table. Réduit la croissance future mais ne réduit pas pg_database_size tout de suite.",
        time: "Généralement quelques secondes à minutes. Coût ≈ taille de la table / IO séquentiel (≈200 Mo/s sur un SSD classique)."
      },
      vacuumFull: {
        flow: "Acquiert un verrou ACCESS EXCLUSIVE, réécrit toute la table (et son TOAST) dans un nouveau fichier, puis échange le nouveau fichier et libère l'ancien.",
        impact: "⚠️ Tant que le verrou ACCESS EXCLUSIVE est maintenu, TOUTES les lectures et écritures sur cette table sont bloquées. À exécuter pendant les heures creuses.",
        disk: "L'usage disque double temporairement (ancien + nouveau). À la fin, l'ancien fichier est libéré. Bloat max récupérable ≈ table + TOAST + index.",
        time: "Coût ≈ (table + index) / IO écriture séquentielle (≈200 Mo/s sur un SSD classique). Exemple : 10 Go ≈ 1–3 min, 100 Go ≈ 15–30 min."
      },
      reindex: {
        flow: "Acquiert un verrou de table et reconstruit chaque index pris en charge (p. ex. B-tree) l'un après l'autre. À la fin, les nouveaux index remplacent les anciens.",
        impact: "⚠️ Le verrou bloque les DML (INSERT / UPDATE / DELETE) pendant la reconstruction. Recommandé en heures creuses.",
        disk: "L'espace des index double temporairement (anciens + nouveaux). À la fin, les anciens index sont libérés. Supprime le bloat d'index et peut accélérer les lectures.",
        time: "Coût ≈ taille totale des index / IO écriture séquentielle (≈200 Mo/s sur un SSD classique). Index de cette table ≈ {size} ; de quelques secondes à minutes."
      }
    }
  },
  hotPartition: {
    hotTableTitle: "Migration des données des hot tables",
    hotTableDesc: "Promeut les lignes des hot tables vers les partitions mensuelles. Un VACUUM FULL est ensuite nécessaire pour vraiment rendre l'espace au système d'exploitation.",
    retention: {
      all: "Tout migrer maintenant",
      "1day": "Conserver 1 jour",
      "3day": "Conserver 3 jours",
      "7day": "Conserver 7 jours",
      "30day": "Conserver 30 jours"
    },
    startMigrate: "Lancer la migration",
    migrating: "Migration…",
    migrationProgress: "{migrated} lignes migrées ({batches} lots, {duration}s)",
    promoteConfirm: "Migrer de {label} les données de plus de {hours} ?",
    promoteAll: "tout",
    days: "{n} jours",
    partitionTitle: "Gestion des partitions mensuelles",
    partitionDesc: "Supprimez les anciennes partitions mensuelles pour libérer de l'espace disque.",
    partitionWarning: "Cette action est irréversible: à utiliser avec précaution.",
    tables: {
      title: "Tables partitionnées",
      refresh: "Actualiser",
      columns: {
        name: "Table",
        description: "Description",
        totalSize: "Taille totale",
        rows: "Lignes",
        partitions: "Partitions",
        archivable: "Archivables",
        actions: "Actions"
      },
      viewPartitions: "Voir / gérer les partitions",
      noTables: "Aucune table partitionnée"
    },
    partitionList: {
      title: "Partitions de {table}",
      columns: {
        name: "Partition",
        storageType: "Stockage",
        size: "Taille",
        rows: "Lignes",
        month: "Mois",
        action: "Actions"
      },
      storage: {
        columnar: "colonnaire",
        heap: "heap",
        archive: "archivée"
      },
      delete: "Supprimer",
      deleting: "Suppression…",
      empty: "Aucune partition pour cette table"
    },
    deleteModal: {
      title: "Confirmer la suppression de la partition",
      warning: "Cette action est irréversible !",
      fields: {
        name: "Partition",
        size: "Taille",
        rows: "Lignes",
        month: "Mois"
      },
      inputLabel: "Saisissez le nom de la partition pour confirmer :",
      cancel: "Annuler",
      confirm: "Supprimer la partition",
      deleting: "Suppression…",
    },
    hotTableNames: {
      requestLogs: "Journaux de requêtes",
      credentialModelIndex: "Index des modèles d'identifiants",
      usageLedger: "Grand livre de consommation",
      routingDecisionLog: "Journal des décisions de routage"
    },
    labels: {
      rows: "Lignes",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'Les statistiques des tables hot n’ont pas pu être chargées : {reason}. Veuillez réessayer.',
    startMigrationFailedWithReason: 'La tâche de migration n’a pas pu démarrer : {reason}. Veuillez réessayer.',
    submitDeleteFailedWithReason: 'La tâche de suppression n’a pas pu être soumise : {reason}. Veuillez réessayer.',
  },
  preview: "Aperçu de l'impact",
  hotData: "Données chaudes (0–7 jours)",
  warmData: "Données tièdes (7–30 jours)",
  coldData: "Données froides (30–90 jours)",
  expiredData: "Données expirées (>90 jours)",
  missingDate: "Veuillez choisir une date de début et une date de fin",
  previewFailed: "Échec de l'aperçu",
  needPreview: "Veuillez d'abord exécuter un aperçu",
  executeNotImpl: "Le nettoyage sera implémenté dans la prochaine phase. Veuillez utiliser les scripts CLI pour l'instant."
,
  pages: {
    filesystemMaintenance: 'Maintenance du système de fichiers',
    storageConfig: 'Configuration du stockage',
    logManagement: 'Gestion des journaux',
    usageCost: 'Coût d’utilisation',
    usageEnhanced: 'Analyse avancée de l’utilisation',
  },

  usageEnhanced: {
    refresh: 'Actualiser',
    loading: 'Chargement…',
    empty: 'Aucun enregistrement d’utilisation sur cette période',
    cards: {
      totalCost: 'Coût total',
      totalRequests: 'Requêtes totales',
      periodDelta: 'Δ de coût entre périodes',
      cacheHitRate: 'Taux de succès du cache',
    },
    trend: {
      title: 'Tendance des coûts ({days} derniers jours)',
    },
    hotKeys: {
      title: 'Principales clés API (par consommation de tokens)',
      colId: 'ID de clé API',
      colPrefix: 'Préfixe de clé',
      colApplication: 'Application utilisée',
      colRequests: 'Requêtes',
      colTokens: 'Nombre de tokens',
      colCost: 'Coût total',
    },
    degraded: {
      title: 'Certaines métriques sont indisponibles : ',
      hint: 'Le schéma de la base de données est en retard sur cette version ; les chiffres sont des substituts « non calculables », pas des mesures réelles.',
    },
    errors: {
      load: 'Échec du chargement des données d’utilisation avancées',
    },
    sources: {
      summary: 'Résumé d’utilisation',
      costTrend: 'Tendance des coûts',
      periodCompare: 'Comparaison de périodes',
      cacheEconomics: 'Économie du cache',
      hotKeys: 'Clés API actives',
    },
    footer: 'Dernière actualisation : {time}',
  },

  usageCost: {
    refresh: 'Actualiser',
    loading: 'Chargement…',
    compare: {
      title: 'Comparaison des coûts',
      currentPeriod: 'Période actuelle :',
      previousPeriod: 'Comparer :',
      currentLabel: 'Actuelle ({period})',
      previousLabel: 'Période de référence ({period})',
      change: 'Variation',
      significant: 'Significatif',
      meta: '{requests} requêtes: {models} modèles',
    },
    attribution: {
      title: 'Attribution des coûts',
      groupBy: 'Grouper par :',
      model: 'Par modèle',
      provider: 'Par fournisseur',
      intent: 'Par intention',
      shareChart: 'Part des coûts',
      detailChart: 'Détail (entrée vs sortie)',
      otherNote: 'Autres {count} éléments : {cost}',
      otherLabel: 'Autres',
      colModel: 'Modèle',
      colProvider: 'Fournisseur',
      colIntent: 'Intention',
      colRequests: 'Requêtes',
      colTotalCost: 'Coût total',
      colInputCost: 'Coût entrée',
      colOutputCost: 'Coût sortie',
      colShare: 'Part',
      colLatency: 'Latence moy.',
    },
    charts: {
      inputCost: 'Coût entrée',
      outputCost: 'Coût sortie',
    },
    cache: {
      title: 'Économie du cache',
      hitRate: 'Taux de succès cache',
      cacheSaved: 'Économies cache',
      cacheSavedHint: 'vs sans cache',
      compressionSaved: 'Économies compression',
      compressionCount: '{n} compressions',
      savingsRate: 'Taux d’économie global',
      totalSaved: 'Total économisé {cost}',
      actualSpend: 'Dépenses réelles',
      effectiveRatio: '{pct} du coût non optimisé',
      totalRequests: 'Requêtes totales',
    },
    errors: {
      costTrend: 'Échec du chargement de la tendance des coûts',
      periodCompare: 'Échec du chargement de la comparaison',
      cacheEconomics: 'Échec du chargement de l’économie cache',
    },
    degraded: {
      title: 'Certaines métriques sont indisponibles :',
      hint: 'Le schéma de la base de données est en retard sur cette version ; les valeurs ci-dessous sont des espaces réservés « non calculables », et non des mesures (ce n\'est pas la même chose qu\'aucune donnée sur cette période). Demandez à un administrateur d\'exécuter la migration d\'agrégation.',
    },
  },
  confirmDeleteCleanup: 'Supprimer {rows} lignes ?\nEspace libéré estimé : {size}\n\nAction irréversible !',
  confirmArchiveCleanup: 'Archiver {rows} lignes ?\nEspace libéré estimé : {size}\n\nAction irréversible !',
  confirmAttachmentNullify: 'Exécuter ?\n{n} éléments seront mis à NULL\nTotal : {size}\n(les fichiers physiques ne sont pas supprimés)',
  confirmBlobCleanup: 'Nettoyer ?\n{rows} lignes affectées\nenviron {size} libérés\n(seuls request_body / outbound_body passent à NULL ; métadonnées conservées)',
  confirmReplay: 'Rejouer {filename} ?',
  confirmReplayArchive: 'Rejouer et archiver {filename} ?',
  confirmFileCleanup: 'Supprimer les fichiers de plus de {days} jours ?\nMotif : {reason}',
  confirmLogCleanup: 'Supprimer les fichiers de journal de plus de {days} jours ? Action irréversible.',
  confirmDirMigrate: 'Le répertoire de stockage des pièces jointes va changer. Les fichiers existants seront copiés vers le nouveau répertoire ; l\'ancien sera supprimé après vérification.\n\n· Téléchargements/écritures non affectés pendant la copie (ancien répertoire)\n· Bascule automatique vers le nouveau répertoire à la fin\n· Peut prendre du temps pour de nombreux fichiers ; progression affichée ci-dessous\n\nChanger le répertoire et migrer ?',
  dirMigrateCancelled: 'Annulé (répertoire inchangé)',
}