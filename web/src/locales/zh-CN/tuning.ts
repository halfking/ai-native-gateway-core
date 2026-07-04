// tuning.ts — TuningView 文案。
// 大部分页面已经是英文，留空的项在 en-US 中也保持一致；这里只覆盖确实有少量中文的位置。
export default {
  title: 'Auto-Route 反馈调优',
  subtitle: 'Tuning feedback loop for the auto-route classifier. The daily analyzer at 02:00 UTC generates proposals from accumulated tuning_signals; admins review and approve below.',
  sections: {
    proposals: '调优提案 (Proposals)',
    accuracy: '准确度仪表盘 (Accuracy Dashboard)',
    strategy: 'A/B 分类策略对比 (Strategy Breakdown)',
    manual: '手动触发分析 (Manual Analyze)',
  },
}