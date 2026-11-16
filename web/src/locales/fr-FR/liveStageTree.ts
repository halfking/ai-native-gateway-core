// liveStageTree.ts — Arbre des étapes de la requête (live2) (2026-11-16)
export default {
  title: 'Arbre des étapes de la requête',
  empty: 'Aucune requête dans la fenêtre avec les filtres actuels',
  stage: {
    auth: 'Authentification / quota',
    sanitize: 'Anonymisation',
    approval: 'Approbation',
    injection: 'Détection d’injection',
    route: 'Routage',
    compress: 'Compactage',
    firstByte: 'Premier octet',
    restore: 'Restauration de l’anonymisation',
    provider: 'Fournisseur / pool de jetons',
    meter: 'Comptage',
    reply: 'Réponse',
  },
  state: {
    pending: 'Pas encore atteinte',
    active: 'En cours',
    done: 'Terminée',
    skipped: 'Non applicable',
    failed: 'En échec',
    warned: 'Avec avertissement',
    unmeasured: 'Non mesurée',
  },
  value: {
    sanitize: '{n} emplacement(s)',
    route: '{n} candidat(s)',
    meter: '{n} jeton',
    injection: 'note {n} ({d})',
    compress: {
      yes: 'Compressée',
      no: 'Non compressée',
    },
  },
  decision: {
    pass: 'Autorisée',
    need_approval: 'En attente d’approbation',
    block: 'Refusée',
    safe: 'Sûr',
    warn: 'Correspondance (avertissement seul)',
    unknown: 'Non déterminée',
  },
  return: {
    lane: 'La réponse revient au client par la voie de retour',
    label: 'Vers le client',
  },
  band: {
    status: {
      success: 'Réussie',
      in_progress: 'En cours',
      failure: 'En échec',
    },
    toolsRestore: 'tools restauré {n}',
    toolsRestored: 'restauré',
    finalModel: 'Modèle final',
    upstreamModel: 'Modèle amont',
    finalModelTip: {
      action: 'Source : l’événement de nœud retenu par le routage (nom standard après résolution).',
      canonical: 'Source : le nom de modèle standard enregistré sur la requête.',
      upstreamRaw: 'Source : la chaîne de modèle réellement envoyée à l’amont.',
      frame: 'Source : le champ modèle de la requête.',
      none: 'Non mesurée : aucune source de modèle déterminable.',
    },
    modelEntry: {
      auto: 'AUTO a choisi {model}',
      direct: 'Demandé {model}',
    },
    chain: {
      auto: 'type de tâche auto {task} → correspondance {model}',
      alias: '{from} a été résolu en {model}',
      switch: 'Changement de modèle {from} → {to}',
    },
    cache: {
      hit: 'Succès de cache {pct}%',
      na: 'Taux de succès de cache —',
      anomaly: 'Base de cache incohérente',
      tip: {
        na: 'L’amont n’a pas indiqué de nombre de succès de cache ; ce n’est pas 0%.',
        unknown: 'Le protocole amont n’est pas déterminé, le dénominateur est inconnu.',
        anomaly: 'Le taux dépasse 100 %, ce qui indique deux bases mélangées.',
        parallel: 'Base : les lectures de cache sont à côté de l’entrée ; dénominateur = entrée + lectures de cache.',
        subset: 'Base : les lectures de cache sont déjà dans l’entrée ; dénominateur = entrée.',
      },
    },
  },
  credibility: {
    verified: 'Fiable',
    suspected: 'Possiblement dilué',
    diluted: 'Dilué',
    unknown: 'Indéterminée',
    unmeasured: 'Fiabilité non mesurée',
    fingerprintMissing: 'Non fourni',
    measuredAtMissing: 'Non fourni',
    tip: {
      unmeasured: 'Il n’y a aucune mesure de fiabilité pour ce couple identifiants / modèle amont.',
      detail:
        'Verdict {verdict} | identité {identity} | capacité {capability} | stabilité {consistency} | référence {reference} | empreinte système {fingerprint} | mesuré le {measuredAt}',
    },
  },
  stats: {
    open: 'Développer les statistiques (page entière)',
    readout: '{total} au total | {rate}% de réussite | {inFlight} en cours',
    title: 'Statistiques en direct',
    hint:
      'Même base que la page de topologie en direct : les 5 dernières minutes et la dernière heure sont lues directement en base, et la journée en cours provient de l’agrégat journalier (jusqu’à 1 heure de retard).',
    close: 'Fermer',
  },
  idle: {
    wait: 'Aucune nouvelle requête depuis {ms} ms',
  },
}
