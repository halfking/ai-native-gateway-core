// autoOps.ts — AUTO 路由运营统一工作台文案（2026-10-02 整合轮）。
export default {
  title: 'AUTO Route Ops',
  desc: 'AUTO routing loop: annotate → stats → tier suggestions → tuning review; changes take effect only via gate + human approval',
  tab: {
    annotate: 'Annotate',
    stats: 'Annotation Stats',
    profiles: 'Task Profiles',
    tuning: 'Tuning Review',
  },
  kpi: {
    pendingToday: 'To annotate today',
    totalAnnotations: 'Total annotations',
    accuracy: 'Classification accuracy',
    annotators: 'Annotators',
    pendingProposals: 'Proposals pending review',
    suggestions: 'Tier suggestion types',
    updatedAt: 'Updated {time}',
    refresh: 'Refresh overview',
    refreshing: 'Refreshing…',
  },
}
