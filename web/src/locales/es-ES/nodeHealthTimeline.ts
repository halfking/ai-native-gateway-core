// nodeHealthTimeline.ts — T9 node recovery timeline view (English)
export default {
  title: 'Node Recovery Timeline',
  subtitle: 'Recent key recovery events for a single credential',
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