// credentialMonitor.ts — 凭据监控页(CredentialMonitorView)文案。
// 命名空间：page / filter / summary / table / drawer / dialog / status / action / error / chart。
// 后端枚举字符串（ready/degraded/healthy/warning/broken 等）保持原样 — 翻译会破坏 API 调用。
// 复用 common/keys 已有的按钮/状态词汇；本页特有术语集中在本文件。
export default {
  page: {
    title: '凭据监控',
  },
  table: {
    cell: {
      modelsNotMeasured: '该凭据本次未返回模型计数（未测量），不是「0 个可用」',
    },
  },
  drawer: {
    tab: {
      overview: '基础信息',
      models: '模型',
      requests: '请求数据',
    },
    operationFailedWithReason: '操作失败：{reason}。请重试',
    offlineFailedWithReason: '下线失败：{reason}。请重试',
    onlineFailedWithReason: '上线失败：{reason}。请重试',
    degradeFailedWithReason: '降级失败：{reason}。请重试',
    upgradeFailedWithReason: '升级失败：{reason}。请重试',
    settingFailedWithReason: '设置失败：{reason}。请重试',
    manualDisabledTooltip: '已通过手工方式禁用，自动探测不会触碰。可手工启动',
    availableTooltip: '当前可用，可手工禁用',
    autoControlledTooltip: '由自动探测控制（broken_confirmed 等）。可手工禁用（强制下线）',
    rowSelectsMonitor: '选中行可联动查看右侧监控',
    selectModelRow: '选中左侧模型行可查看',
  },
  chart: {
    allHealthy: '全部健康',
    errorsWhenHealthy: '错误类型分布：全部健康',
    errorsTitle: '错误类型分布',
  },
  error: {
    selectFirst: '请先选择凭据',
    clearFailed: '清除失败: ', // 后面接 e.message
    batchFailed: '批量操作失败: ',
    superAdminOnlyHint: '仅超级管理员可执行状态修正；当前为只读。',
    superAdminOnlyError: '仅超级管理员可执行状态修正操作。',
  },
}