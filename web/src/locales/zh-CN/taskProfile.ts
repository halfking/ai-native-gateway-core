// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: '刷新',
    refreshing: '刷新中…',
    loadFailed: '加载失败',
    noData: '暂无档案数据',
  registry: {
    version: '档案版本',
    types: '任务类型数',
    correctedTotal: '窗口内修正总数',
    pendingSuggestions: '待裁决升档',
  },
  table: {
    taskType: '任务类型',
    description: '描述',
    tier: '当前档位',
    fallbacks: '降级链',
    minConf: '最低置信度',
    total: '修正样本',
    rate: '修正率',
    suggestion: '建议档位',
  },
  action: {
    apply: '应用升档建议',
    applyConfirm: '把修正驱动的分层建议写入 tier 配置（task_type_tier_config）？',
    applyDone: '已应用：{types}',
    applyNone: '当前没有满足阈值的升档建议',
    reload: '重载 overlay 档案',
    reloadConfirm: '重新加载 TASKPROFILE_OVERLAY 档案文件？未配置时复位为内嵌默认。',
    reloadDone: '档案已重载：{version}',
    exportCsv: '导出修正 CSV',
    days: '统计窗口(天)',
  },
  status: {
    applying: '应用中…',
    reloading: '重载中…',
    exporting: '导出中…',
  },
}
