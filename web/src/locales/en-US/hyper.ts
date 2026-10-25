// hyper.ts — Hyper shell strings (bottom nav / account sheet / list state / focus / desktop-only).
// Added 2026-10-04 with the Hyper runtime (docs/UI规范/00 §5.4, H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'Primary navigation',
    more: 'More',
  },
  account: {
    title: 'Account',
    language: 'Language',
    theme: 'Theme',
    help: 'Help',
    health: 'Service health',
    healthy: 'Reachable',
    unhealthy: 'Unreachable',
    unknown: 'Status unknown',
    logout: 'Sign out',
    adminEntry: 'Admin console',
    close: 'Close',
  },
  dataView: {
    table: 'Table',
    cards: 'Cards',
  },
  list: {
    refreshing: 'Refreshing…',
    loadMore: 'Load more',
    loadingMore: 'Loading…',
    retry: 'Retry',
    allLoaded: 'All {count} loaded',
    loadFailed: 'Failed to load. Select Retry to try again.',
  },
}
