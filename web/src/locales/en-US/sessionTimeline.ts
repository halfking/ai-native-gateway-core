// sessionTimeline.ts — SessionTurnsTimeline.vue copy (OBS-FE5 turn timeline)
// Covers: latency null fallback / refresh / error / empty / load more / all loaded / network error
export default {
  latencyUnknown: 'Unknown',
  refresh: 'Refresh',
  refreshing: 'Refreshing…',
  retry: 'Retry',
  empty: 'No turns in this session',
  loading: 'Loading…',
  loadMore: 'Load more turns',
  allLoaded: '{n} turns loaded, all complete',
  errors: {
    network: 'Network error, please check connection and retry',
  },
}
