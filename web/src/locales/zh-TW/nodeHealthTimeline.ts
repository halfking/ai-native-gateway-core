// nodeHealthTimeline.ts — T9 node recovery timeline view (English)
export default {
  title: 'Node Recovery Timeline',
  subtitle: '單一憑證近期的金鑰復原事件',
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