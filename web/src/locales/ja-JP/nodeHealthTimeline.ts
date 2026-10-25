// nodeHealthTimeline.ts — T9 node recovery timeline view (English)
export default {
  title: 'Node Recovery Timeline',
  subtitle: '1 つの資格情報に関する最近の鍵復旧イベント',
  loading: 'Loading recovery timeline…',
  error: 'Failed to load recovery timeline',
  empty: 'No recovery events for this credential',
  observationDegraded: 'Observation degraded',
  reason: 'Reason {code}',
  duration: 'Duration {ms}',
  events: {
    failed: 'Failed',
    probing: 'Probing',
    recovered: 'Recovered',
    degraded: 'Degraded',
    reconnected: 'Reconnected',
    quarantined: 'Quarantined',
  },
}