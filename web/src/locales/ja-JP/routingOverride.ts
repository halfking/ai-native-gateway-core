// Auto-synced from en-US (ja-JP)
export default {
  title: 'Routing Overrides',
  subtitle: '(task_type, profile) の組合せごとに特定のモデルを固定または禁止できます。変更は OverrideStore の再読込から 1 分以内に反映されます。',
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
    hintPin: 'Pin: スコアに関わらず候補の先頭に固定します。',
    hintBan: 'Ban: 候補から完全に除外します。',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'フィルタに一致するオーバーライドはありません。「+ 新規オーバーライド」を選んで作成してください。',
    deleteConfirm: 'オーバーライド #{id}（{task} の {mode} {model}）を削除しますか？',
    deleteFailed: 'Delete failed: ',
  },
}
