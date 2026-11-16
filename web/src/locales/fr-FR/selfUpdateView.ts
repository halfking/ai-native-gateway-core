// selfUpdateView.ts — Mise à jour automatique et montée de version (SelfUpdateView).
//
// Regle de formulation (meme source que le code de la page) : echec de la
// verification, absence de source distante configuree ou verification jamais
// executee signifient "conclusion impossible". La page affiche alors un
// statut inconnu avec la raison, et ne pretend jamais que la version actuelle
// est la plus recente.
export default {
  pageTitle: 'Mise à jour automatique et montée de version',
  pageSubtitle:
    'Le nœud recherche seul les nouvelles versions, émet un rappel et installe la mise à jour après validation par un administrateur',

  statusCardTitle: 'État de la version en cours',
  statusCardHint:
    'Affiche la version exécutée par ce nœud et le résultat de la dernière recherche de version distante',
  currentVersion: 'Version actuelle',
  latestVersion: 'Dernière version disponible',
  buildSequence: 'Numéro de build',
  lastCheckedAt: 'Dernière vérification',
  checkInterval: 'Intervalle de vérification',
  checkIntervalHours: 'Vérifie automatiquement toutes les {hours} heures',
  checkIntervalMinutes: 'Vérifie automatiquement toutes les {minutes} minutes',
  checkIntervalSeconds: 'Vérifie automatiquement toutes les {seconds} secondes',
  checkNow: 'Vérifier les mises à jour maintenant',
  checking: 'Vérification en cours',

  stateNotConfiguredLabel: 'Aucune source de version distante configurée',
  stateNotConfiguredDetail:
    'Aucune source de version distante n’est configurée sur ce nœud : la vérification automatique et les rappels sont désactivés. Impossible de déterminer si une nouvelle version existe.',
  stateUnknownLabel: 'État de la version inconnu',
  stateUnknownDetail:
    'La recherche de version n’a pas abouti : {reason}. Tant qu’une vérification n’aboutit pas, impossible de déterminer si une nouvelle version existe.',
  stateNeverCheckedDetail:
    'Une source de version distante est configurée sur ce nœud, mais aucune recherche de version n’a encore été exécutée. Impossible de déterminer si une nouvelle version existe.',
  stateUpToDateLabel: 'Déjà à jour',
  stateUpToDateDetail:
    'La recherche distante a confirmé qu’aucune nouvelle version n’est disponible.',
  stateAvailableLabel: 'Une nouvelle version est disponible',
  stateAvailableDetail:
    'La recherche distante a confirmé que la version {version} est disponible et peut être installée.',

  bannerTitle: 'Une nouvelle version est disponible',
  bannerMandatory: 'Mise à jour obligatoire',
  bannerOptional: 'Mise à jour facultative',
  bannerNotesTitle: 'Notes de version',
  bannerNoNotes: 'Cette version ne fournit aucune note de version.',
  installNow: 'Mettre à jour maintenant',

  confirmInstallTitle: 'Confirmer la mise à jour vers {version}',
  confirmInstallBody:
    'La mise à jour télécharge le nouveau binaire, le vérifie, remplace le binaire actuel puis redémarre le service. Les requêtes sont brièvement interrompues pendant le redémarrage. Continuer ?',
  confirmInstallButton: 'Confirmer la mise à jour',
  installStartFailed: 'La tâche d’installation n’a pas pu démarrer : {reason}',

  installCardTitle: 'Progression de l’installation',
  installCardHint:
    'Affiche l’état et les étapes exécutées de la dernière tâche d’installation',
  installCardEmpty: 'Aucune tâche d’installation n’est encore enregistrée sur ce nœud.',
  jobVersion: 'Version cible',
  jobMode: 'Méthode d’installation',
  jobModeUnknown: 'Méthode d’installation inconnue',
  jobStatus: 'État de la tâche',
  jobStatusPending: 'En attente de démarrage',
  jobStatusDownloading: 'Téléchargement de l’artefact',
  jobStatusReadyToRestart: 'En attente du redémarrage du service',
  jobStatusUpgrading: 'Remplacement du binaire',
  jobStatusSuccess: 'Mise à jour réussie',
  jobStatusFailed: 'Échec de la mise à jour',
  jobStatusRolledBack: 'Retour à la version précédente',
  jobStatusUnknown: 'État inconnu',
  jobStartedAt: 'Heure de début',
  jobStepsTitle: 'Étapes exécutées',
  jobStepsEmpty: 'La tâche n’a pas encore produit d’étape.',
  jobErrorTitle: 'Cause de l’échec',
  jobSuccessNotice: 'La mise à jour est terminée et la nouvelle version est active.',
  jobRestartScheduled:
    'La mise à jour est terminée, le service redémarrera sous peu.',
  modeArtifact: 'Télécharger l’artefact et installer',
  modeSource: 'Compiler depuis les sources locales et installer',

  notificationsTitle: 'Rappels de mise à jour',
  notificationsHint:
    'Affiche les rappels créés lors de la détection d’une nouvelle version',
  notificationsEmpty: 'Aucun rappel de mise à jour n’est en attente de traitement.',
  notificationsLoadFailed:
    'La liste des rappels n’a pas pu être lue : {reason}',
  notificationVersion: 'Version',
  notificationStatus: 'État du rappel',
  notificationStartedAt: 'Créé le',
  notificationCompletedAt: 'Traité le',
  notificationRetryCount: 'Nombre de tentatives',
  notificationPending: 'En attente de traitement',
  notificationHandled: 'Traité',

  sourceCardTitle: 'Compiler depuis les sources locales et mettre à jour',
  sourceCardHint:
    'Compile le binaire à partir des sources présentes sur cette machine et l’installe. La compilation ne modifie pas l’arbre de travail actuel.',
  sourceRefLabel: 'Référence de la source',
  sourceRefPlaceholder:
    'Laisser vide pour utiliser l’arbre de travail actuel',
  sourceBuildNow: 'Compiler depuis les sources et mettre à jour',
  confirmSourceBuildTitle:
    'Confirmer la compilation depuis les sources et la mise à jour',
  confirmSourceBuildBody:
    'La compilation récupère un arbre de travail distinct, crée le binaire, remplace la version actuelle puis redémarre le service. Elle utilise le processeur et le disque de cette machine. Continuer ?',
  confirmSourceBuildButton: 'Confirmer la compilation et la mise à jour',
  sourceBuildNoVersion:
    'La version à installer n’est pas encore connue. Effectuez d’abord une recherche de version.',
  sourceBuildFailed: 'La tâche de compilation n’a pas pu démarrer : {reason}',
}
