// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'Streaming client SSE connections (process-local live + recent closed audit)',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: 'Closed records are process-local audit only (not persisted)',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'Filter list (request_id / protocol / client)…',
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
  viewTimeline: 'View node recovery timeline',
  recoverAt: 'Recover at {time} ({delta})',
  detail: {
    lastError: 'Last error',
    lastErrorAt: 'Last error at',
    recoverAt: 'Recover at',
  },
  state: {
    connected: 'Connected',
    connecting: 'Connecting',
    disconnected: 'Disconnected',
  },
  inFlight: 'In flight {count}',
}
