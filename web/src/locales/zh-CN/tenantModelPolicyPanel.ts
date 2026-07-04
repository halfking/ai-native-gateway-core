// Chinese (Simplified) locale for TenantModelPolicyPanel
export default {
  title: '模型管控',
  hint: '在此配置的模型将被该租户下的所有 API key 拒绝（403 model_forbidden）。空表 = 该租户无限制（默认）。model="auto" 路径不纳入管控。',
  showDeleted: '显示已删除',
  addButton: '+ 添加禁用模型',
  loading: '加载中…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: '操作',
  },
  
  actions: {
    softDelete: '软删除',
    restore: '恢复',
  },
  
  empty: '无策略（默认所有模型允许）',
  
  audit: {
    title: '审计日志',
    recent: '最近 {count} 条',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: '创建',
      update: '修改',
      delete: '软删除',
      undelete: '恢复',
    },
  },
  
  dialog: {
    title: '添加禁用模型',
    hint: '在下方输入 canonical_name（必须匹配 models_canonical 表）。',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: '例如 minimax-m3',
    checkButton: '校验',
    checkSuccess: '✓ 找到于 models_canonical（family={family}, modality={modality}）',
    checkWarning: '⚠ 该 canonical_name 不在 models_canonical（仍允许写入，防御性管控）',
    reason: 'reason',
    reasonPlaceholder: '可选，例如 成本控制',
    cancel: '取消',
    submit: '提交',
    submitting: '提交中…',
  },
  
  confirm: {
    softDelete: '确认软删除策略 {name}？(可恢复)',
  },
  
  error: {
    loadFailed: '加载失败',
    canonicalNameRequired: 'canonical_name 必填',
    createFailed: '创建失败',
    deleteFailed: '删除失败',
    restoreFailed: '恢复失败',
  },
}
