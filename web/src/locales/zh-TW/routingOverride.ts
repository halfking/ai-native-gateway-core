// Auto-synced from en-US (zh-TW)
export default {
  title: 'Routing Overrides',
  subtitle: '可依 (task_type, profile) 組合將特定模型釘選或封鎖。變更會在 1 分鐘內由 OverrideStore 重新整理後生效。',
  correlationsLink: 'Correlations dashboard',
  summary: {
    total: 'Total active',
    bans: 'Bans',
    pins: 'Pins',
    expiring: 'Expiring in 7d',
  },
  filter: {
    activeOnly: 'Active only',
    taskType: 'Task type',
    taskTypePlaceholder: 'e.g. code, reasoning',
    profile: 'Profile',
    all: '(all)',
    refresh: 'Refresh',
    loading: 'Loading…',
    newOverride: '+ New override',
    cancel: 'Cancel',
  },
  create: {
    title: 'New routing override',
    hintPin: '釘選：模型一律排到候選最前（不看分數）。',
    hintBan: '封鎖：模型完全不列入候選。',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: '沒有覆寫符合篩選條件。點「+ 新增覆寫」建立一筆。',
    deleteConfirm: '要刪除覆寫 #{id}（{task} 的 {mode} {model}）嗎？',
    deleteFailed: 'Delete failed: ',
  },
}
