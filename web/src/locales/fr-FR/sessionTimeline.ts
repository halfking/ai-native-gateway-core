// sessionTimeline.ts — SessionTurnsTimeline.vue textes (OBS-FE5 chronologie des tours)
// Couvre : latence nulle / actualiser / erreur / vide / charger plus / tout chargé / erreur réseau
export default {
  latencyUnknown: 'Inconnu',
  refresh: 'Actualiser',
  refreshing: 'Actualisation…',
  retry: 'Réessayer',
  empty: 'Aucun tour dans cette session',
  loading: 'Chargement…',
  loadMore: 'Charger plus de tours',
  bodyUnavailable: 'Aucun corps de requête/réponse n\'a été capturé pour {n} des tours chargés ; le stockage des corps est peut-être désactivé pour cette session.',
  allLoaded: '{n} tours chargés, terminés',
  errors: {
    network: 'Erreur réseau, vérifiez la connexion et réessayez',
  },
}
