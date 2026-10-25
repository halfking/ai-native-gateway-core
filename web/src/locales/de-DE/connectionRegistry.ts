// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'SSE-Verbindungen von Streaming-Clients (prozesslokal live, abgeschlossene Einträge protokolliert)',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: 'Geschlossene Einträge werden nur prozesslokal protokolliert, nicht dauerhaft gespeichert',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'Liste filtern (request_id / Protokoll / Client)…',
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
