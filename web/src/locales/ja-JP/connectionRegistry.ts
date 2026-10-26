// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'ストリーミングクライアントの SSE 接続（プロセス内の稼働状況と最近の終了監査）',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: '終了した記録はプロセス内の監査のみで、保存されません',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'リストを絞り込む（request_id / プロトコル / クライアント）…',
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
