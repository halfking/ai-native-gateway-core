// modulesView.ts — モジュール管理ページの文案。
export default {
  pageTitle: 'モジュール管理',
  pageSubtitle: 'エンタープライズ機能モジュールの集中管理 — 必要に応じて機能を有効/無効にします。',
  modulesEnabled: 'モジュールが有効です',
  loading: '読み込み中…',

  category: {
    compression: 'リクエスト圧縮',
    session: 'セッション管理',
    security: 'セキュリティ',
    rate_limit: 'レート制限',
    general: '一般',
    integration: '連携',
  },

  status: {
    enabled: '有効',
    disabled: '無効',
    processing: '処理中…',
    enabledAction: 'このモジュールを無効化',
    disabledAction: 'このモジュールを有効化',
  },

  dangerLevel: {
    safe: '安全',
    warn: '注意',
    danger: '危険',
    breaking: '重大',
    unknown: '不明',
  },

  tabs: {
    overview: '概要',
    config: '設定',
    integration: '連携',
    status: 'ランタイム',
  },

  overview: {
    sectionDescription: '説明',
    sectionCapabilities: '機能一覧',
    labelKey: 'モジュールキー',
    labelDanger: '危険度',
    labelConfigCount: '設定項目数',
    labelStatus: '現在の状態',
    viewAllSettings: 'すべてのシステム設定を表示',
    testConnection: '接続テスト',
    testSuccess: '接続テスト成功',
    testFailed: '接続テスト失敗',
    testInProgress: 'テストメッセージを送信中…',
    dependenciesTitle: "依存モジュール",
    required: "必須",
    optional: "任意",
    enableAllDeps: "必須依存 {count} 件を一括で有効化",
    enableAllDepsHint: "バックエンドが未有効の必須依存を安定した順序で有効化したうえで、このモジュールを有効化します",
    cascading: "依存モジュールを有効化しています…",
    cascadeSuccess: "{count} 件の依存モジュールを自動有効化しました：{names}",
    cascadeEmpty: "すべての依存モジュールが有効化済みで、このモジュールも正常に有効になりました",
    cascadeAlreadyEnabled: "モジュールは既に有効化済みです。依存の連鎖有効化は不要です",
    cascadeLogTitle: "今回連鎖で有効化した依存モジュール：",

    sectionDependencies: "依存モジュール",

    dependencyDisabled: "依存モジュールが未有効です",

    notEnabled: "未有効",
  },

  config: {
    noSettings: 'このモジュールには設定可能な項目がありません。',
    sourceDefault: 'デフォルト',
    sourceEnv: '環境変数',
    sourceDb: 'データベース',
    switchOn: 'オン',
    switchOff: 'オフ',
    inputPlaceholder: '{description}を入力',
    sections: {
      connection: '接続',
      alerts: 'アラート転送',
      approvals: '承認通知',
      commands: 'コマンドパネル',
      security: 'セキュリティ',
      general: '一般',
    },
  },

  feishu: {
    connectionHint: 'Feishuオープンプラットフォームでカスタムボットを作成し、Webhook URLを下記に貼り付けてください。詳細はFeishu公式ドキュメントを参照してください。',
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'メインスイッチと起動モード: セッション引き継ぎを有効にするかどうか、いつ発生するかを決めます',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 つの起動条件は並行して判定されます。どれか 1 つでも成立すれば引き継ぎが発生し、条件の厳しい方が優先されます。',
    groupSummary: 'Summary generation',
    groupSummaryHint: '要約エンジンとプロンプトテンプレート: 古いセッションを新しいセッションが読み取れるコンテキストへどう圧縮するか',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'クールダウン、セッションごとの上限、失敗時の再試行、ログや Webhook での通知',
  },

  integration: {
    docsLabel: 'ドキュメント: ',
    stepsTitle: 'セットアップ手順',
    enabledStatus: '連携が有効です',
    disabledHint: '連携が無効です — まずモジュールを有効にしてください',
    feishuSteps: [
      'Feishuオープンプラットフォームでカスタムボットを作成する',
      'Webhook URLをコピーして下記設定に貼り付ける',
      '（任意）署名検証トークンと暗号化キーを設定する',
      '設定後「接続テスト」をクリックして検証する',
      '"Feishuボット連携"スイッチをオンにする',
    ],
    feishuBotIntegration: 'Feishuボット連携',
    wechatSteps: [
      'WeCom管理コンソールで自社アプリを作成',
      '（任意）「メッセージ受信」でコールバックURLとTokenを設定',
      'CorpID、AgentID、Secretを下の設定にコピー',
      '（任意）グループボットのWebhook URLを設定',
      '（任意）コールバック暗号化のEncodingAESKeyを設定',
      '「WeChatボット統合」スイッチをオンにする',
      '前提モジュール（圧縮管理、注入検出、セッションキャッシュ、セッション監査）がすべて有効であることを確認',
    ],
    wechatBotIntegration: 'WeChatボット統合',
    dingtalkSteps: [
      'DingTalkグループに「カスタムボット」を追加し、セキュリティ「署名」を選択してWebhookをコピー',
      '下部の設定にWebhook URLを貼り付け、署名Secretを入力',
      '（任意）開発者コンソールで社内アプリを作成し、AppKey/AppSecret/AgentIDを入力してワーク通知を有効化',
      '承認コールバックURL（/api/webhooks/dingtalk/approval-callback）をボットのメッセージ受信先に設定',
      '「DingTalkボット統合」スイッチを有効化',
      '前提モジュール（圧縮、注入検知、セッションキャッシュ、セッション監査）が有効であることを確認',
    ],
    dingtalkBotIntegration: 'DingTalkボット統合',  },

  empty: {
    selectModule: '詳細と設定を表示するモジュールを選択してください',
  },


  error: {
    loadFailed: 'モジュール一覧の読み込みに失敗しました',
    operationFailed: '操作に失敗しました',
    saveFailed: '設定の保存に失敗しました',
    testFailed: 'テスト失敗',
  },

  // 同步的扁平键

  modeBannerTitle: "このページで行えるのは次の 3 つだけです：",

  modeBannerBody: "導入済みモジュールの有効/無効切り替えと、ライセンス済みだが未導入のモジュールのダウンロードに対応します。本ページに価格・注文・プランの入口はありません（購入手順は別途案内します）。",
}