// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'Connexions SSE des clients de streaming (actif en mémoire, audit des fermetures récentes)',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: 'Les enregistrements clos sont audités en mémoire seulement, ils ne sont pas conservés',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'Filtrer la liste (request_id / protocole / client)…',
  sections: {
    live: 'Live connections',
    closed: 'Recently closed',
  },
  columns: {
    requestId: 'Request ID',
    protocol: 'Protocol',
    client: 'Client',
    frames: 'Frames',
    lastFrame: 'Last frame',
    closeReason: 'Close reason',
    registeredAt: 'Registered at',
  },
  empty: {
    live: 'No live streaming connections',
    closed: 'No recently closed connections',
  },
}
