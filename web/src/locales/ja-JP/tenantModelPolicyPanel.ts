// Japanese locale for TenantModelPolicyPanel
export default {
  title: 'モデルアクセス制御',
  hint: 'ここで設定されたモデルは、このテナント配下のすべての API key から拒否されます（403 model_forbidden）。空のテーブル = このテナントに制限なし（デフォルト）。model="auto" パスは管理対象外です。',
  showDeleted: '削除済みを表示',
  addButton: '+ 拒否モデルを追加',
  loading: '読み込み中…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: '操作',
  },
  
  actions: {
    softDelete: '論理削除',
    restore: '復元',
  },
  
  empty: 'ポリシーなし（デフォルトですべてのモデルが許可されます）',
  
  audit: {
    title: '監査ログ',
    recent: '最近の {count} 件',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: '作成',
      update: '更新',
      delete: '論理削除',
      undelete: '復元',
    },
  },
  
  dialog: {
    title: '拒否モデルを追加',
    hint: '以下に canonical_name を入力してください（models_canonical テーブルと一致する必要があります）。',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: '例: minimax-m3',
    checkButton: '検証',
    checkSuccess: '✓ models_canonical で見つかりました（family={family}, modality={modality}）',
    checkWarning: '⚠ この canonical_name は models_canonical にありません（書き込みは許可されます、防御的制御）',
    reason: 'reason',
    reasonPlaceholder: 'オプション、例: コスト管理',
    cancel: 'キャンセル',
    submit: '送信',
    submitting: '送信中…',
  },
  
  confirm: {
    softDelete: 'ポリシー {name} を論理削除しますか？（復元可能）',
  },
  
  error: {
    loadFailed: '読み込みに失敗しました',
    canonicalNameRequired: 'canonical_name は必須です',
    createFailed: '作成に失敗しました',
    deleteFailed: '削除に失敗しました',
    restoreFailed: '復元に失敗しました',
  },
}
