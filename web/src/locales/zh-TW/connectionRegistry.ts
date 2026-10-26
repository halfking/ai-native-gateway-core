// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: '串流客戶端的 SSE 連線（行程內即時狀態 + 近期關閉稽核）',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: '已關閉的紀錄僅存在於行程內稽核，不會落地保存',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: '篩選清單（request_id / 協定 / 客戶端）…',
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
