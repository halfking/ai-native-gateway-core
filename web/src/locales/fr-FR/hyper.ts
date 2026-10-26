// hyper.ts — Libellés du shell Hyper (nav basse / feuille compte / état liste / focus / desktop).
// 2026-10-04, ajoutés avec le runtime Hyper (docs/UI规范/00 §5.4, H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'Navigation principale',
    more: 'Plus',
  },
  account: {
    title: 'Compte',
    language: 'Langue',
    theme: 'Thème',
    help: 'Aide',
    health: 'État du service',
    healthy: 'Joignable',
    unhealthy: 'Injoignable',
    unknown: 'État inconnu',
    logout: 'Se déconnecter',
    adminEntry: 'Console d’administration',
    close: 'Fermer',
  },
  dataView: {
    table: 'Tableau',
    cards: 'Cartes',
  },
  list: {
    refreshing: 'Actualisation…',
    loadMore: 'Charger plus',
    loadingMore: 'Chargement…',
    retry: 'Réessayer',
    allLoaded: '{count} éléments chargés',
    loadFailed: 'Échec du chargement. Veuillez réessayer.',
  },
}
