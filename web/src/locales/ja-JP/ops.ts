export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'データの読み込みに失敗しました',
    fillRequired: '必須項目を入力してください',
    projects: 'プロジェクト',
    sessions: 'セッション',
    codeReviews: 'コードレビュー',
    createProject: 'プロジェクトを作成',
    createProjectTitle: '新しいプロジェクトを作成',
    createProjectSuccess: 'プロジェクトを作成しました',
    createProjectFailed: 'プロジェクトの作成に失敗しました',
    newSession: '新しいセッション',
    createSessionTitle: '新しいセッションを作成',
    createSessionSuccess: 'セッションを作成しました',
    createSessionFailed: 'セッションの作成に失敗しました',
    viewSessions: 'セッションを表示',
    viewReviews: 'レビューを表示',
    showAll: 'すべて表示',
    reviewDetail: 'レビューの詳細',
    projectName: 'プロジェクト名',
    projectNamePlaceholder: 'プロジェクト名を入力',
    language: '言語',
    languagePlaceholder: '例: TypeScript',
    framework: 'フレームワーク',
    frameworkPlaceholder: '例: Vue 3',
    projectId: 'プロジェクト ID',
    sessionName: 'セッション名',
    taskType: 'タスク種別',
    taskTypePlaceholder: '例: コードレビュー、リファクタリング、解説',
    startedAt: '開始日時',
    endedAt: '終了日時',
    filePath: 'ファイルパス',
    score: 'スコア',
    summary: 'サマリー',
    issues: '課題',
    suggestions: '提案',
    category: 'カテゴリ',
    reviewedAt: 'レビュー日時',
    line: '行',
    severity: '重要度',
    message: 'メッセージ',
    /**
     * 空状態（2026-10-27 第 14 インクリメント · 空状態 5 分）。
     * ★ 以前は 3 つの表とも `hyper.list.empty`（このリストにはまだ内容がありません）を共有
     *   したが、実は**2 種類**の状態：
     *   · projects —— 元の配列を使う ⇒ 空 = まだプロジェクトを 1 つも作っていない（FirstUse）
     *   · sessions / reviews —— **絞り込み後**の配列を使う ⇒ 空 = 絞り込みで落ちた（Filtered）
     *   ⇒ 1 文で 2 種類を覆うと、「まだ始めていない」と「絞り込みが狭すぎる」が
     *     同じ話になってしまう。
     */
    emptyFirstUseProjects: 'まだプロジェクトを1つも作成していません。',
    emptyFilteredSessions: '現在のフィルター条件に一致するセッションの記録はありません。',
    emptyFilteredReviews: '現在のフィルター条件に一致するコードレビューの記録はありません。',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: 'ノードの保守操作はスーパー管理者のみ可能です。読み取り専用で表示しています。',
    nodeDetailDefaultTenantOnlyHint: 'default テナントのみノードの保守操作が可能です。読み取り専用で表示しています。',

















    pingFailedWithReason: 'セッション Ping に失敗しました：{reason}。再試行してください。',
    saveSettingFailedWithReason: '設定を保存できませんでした：{reason}。再試行してください。',
    maintainFailed: 'メンテナンスを完了できませんでした。再試行してください。',
    modelMaintainFailed: 'モデルメンテナンスを完了できませんでした。再試行してください。',
    fpSlotLoadFailed: 'フィンガープリントスロットを読み込めませんでした。再試行してください。',
    fpSlotProviderMissing: 'provider_id が不足しているため、フィンガープリントスロットを読み込めませんでした。先にプロバイダーを選択してから再試行してください',
    accessErrorsHint: '失敗した呼び出し + ルーティングエラー · 元のリクエスト詳細を開く',
    recentRequestsHint: '直近 1 時間の{source} · リクエスト詳細を表示',
  }
,

}
