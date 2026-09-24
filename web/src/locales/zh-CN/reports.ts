// reports.ts — 对账报表页（供应商/内部双视角，R65 i18n 补齐）。
// 键与 views/admin/ReconciliationReport.vue 一一对应，默认文案以 Vue 内为准。
export default {
  // 视图切换
  providerView: '供应商对帐',
  internalView: '内部对帐',
  // 工具栏动作
  exportExcel: '导出 Excel',
  rerun: '重跑结束日',
  rerunDone: '重跑完成',
  rerunFailed: '重跑失败',
  // 覆盖范围 / 空态
  daysCovered: '天快照',
  noSnapshots: '该区间没有报表快照（每日聚合任务在凌晨生成前一日数据，或用「重跑结束日」补算）',
  // 汇总卡片
  requests: '请求数',
  totalTokens: '总 tokens',
  in: '入',
  out: '出',
  cacheRead: '缓存读',
  cacheWrite: '缓存写',
  providerCost: '供应商成本',
  cacheHit: '缓存命中',
  internalCredits: '内部积分',
  internalCost: '内部金额',
  // 分组表标题
  byProvider: '按供应商',
  byTenant: '按租户',
  byPerson: '按人员',
  byModel: '按模型',
  byDay: '按天',
  // 列名
  provider: '供应商',
  tenant: '租户',
  person: '人员',
  model: '模型',
  date: '日期',
  success: '成功',
  errors: '失败',
  cost: '成本',
  errorBreakdown: '失败原因分布',
}
