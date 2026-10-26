// connectionRegistry.ts — connection registry view
export default {
  title: 'Connection Registry',
  subtitle: 'اتصالات SSE لعملاء البث (نشطة داخل العملية مع تدقيق إغلاقات حديثة)',
  apiDegraded: 'Connection registry API fetch failed',
  liveCount: 'Live {count} / capacity {capacity}',
  historyNote: 'السجلات المغلقة تُدقَّق داخل العملية فقط ولا تُحفظ',
  search: 'Lookup',
  openJourney: 'Open journey',
  searchPlaceholder: 'Enter request_id to lookup connection…',
  filterPlaceholder: 'تصفية القائمة (request_id / البروتوكول / العميل)…',
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
