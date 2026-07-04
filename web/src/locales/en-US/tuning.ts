// tuning.ts — TuningView strings.
// Most of the page is already English; this file mirrors the same structure
// for the few Chinese section titles that appear in the original template.
export default {
  title: 'Auto-Route feedback tuning',
  subtitle: 'Tuning feedback loop for the auto-route classifier. The daily analyzer at 02:00 UTC generates proposals from accumulated tuning_signals; admins review and approve below.',
  sections: {
    proposals: 'Proposals',
    accuracy: 'Accuracy dashboard',
    strategy: 'A/B strategy breakdown',
    manual: 'Manual analyze',
  },
}