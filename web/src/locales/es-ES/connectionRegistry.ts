// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'Conexiones SSE de clientes de streaming (en vivo en el proceso y auditoría de cierres recientes)',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: 'Los registros cerrados solo se auditan dentro del proceso, no se persisten',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'Filtrar la lista (request_id / protocolo / cliente)…',
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
