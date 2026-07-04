// Chinese (Traditional) locale for TenantModelPolicyPanel
export default {
  title: '模型管控',
  hint: '在此配置的模型將被該租戶下的所有 API key 拒絕（403 model_forbidden）。空表 = 該租戶無限制（預設）。model="auto" 路徑不納入管控。',
  showDeleted: '顯示已刪除',
  addButton: '+ 新增禁用模型',
  loading: '載入中…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: '操作',
  },
  
  actions: {
    softDelete: '軟刪除',
    restore: '復原',
  },
  
  empty: '無策略（預設所有模型允許）',
  
  audit: {
    title: '稽核日誌',
    recent: '最近 {count} 條',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: '建立',
      update: '修改',
      delete: '軟刪除',
      undelete: '復原',
    },
  },
  
  dialog: {
    title: '新增禁用模型',
    hint: '在下方輸入 canonical_name（必須匹配 models_canonical 表）。',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: '例如 minimax-m3',
    checkButton: '校驗',
    checkSuccess: '✓ 找到於 models_canonical（family={family}, modality={modality}）',
    checkWarning: '⚠ 該 canonical_name 不在 models_canonical（仍允許寫入，防禦性管控）',
    reason: 'reason',
    reasonPlaceholder: '可選，例如 成本控制',
    cancel: '取消',
    submit: '提交',
    submitting: '提交中…',
  },
  
  confirm: {
    softDelete: '確認軟刪除策略 {name}？(可復原)',
  },
  
  error: {
    loadFailed: '載入失敗',
    canonicalNameRequired: 'canonical_name 必填',
    createFailed: '建立失敗',
    deleteFailed: '刪除失敗',
    restoreFailed: '復原失敗',
  },
}
