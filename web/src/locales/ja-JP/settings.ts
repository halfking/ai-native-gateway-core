// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — SettingsView 文案。名前空間:category / list / detail / editor / docs / errors。
// 技術フィールド名 (JSON / RPM / TPM / WebSocket など) は翻訳しません。
export default {
  category: {
    all: 'すべて',
    compression: '圧縮',
    rateLimit: 'レート制限',
    timeout: 'タイムアウト',
    retry: 'リトライとタイムアウト',
    routing: 'ルーティング',
    session: 'セッション',
    security: 'セキュリティ',
    outputCompliance: '出力コンプライアンス',
    circuitBreaker: 'サーキットブレーカー',
    loopGuard: 'ループガード',
    general: 'その他',
  },
 list: {
 count: '全 {n}件の設定',
 empty: 'このカテゴリに設定はありません',
 colSetting: '設定',
 colCurrentValue: '現在値',
 colSource: '取得元',
 colDanger: '危険度',
 },
  dangerLevel: {
    note: '🟡 注意',
    warn: '🟠 警告',
    danger: '🔴 危険',
  },
  detail: {
 tenantScopedTitle: 'テナントレベル設定',
 tenantScopedBody: 'この設定は単一テナントに適用され、システムレベルでは設定できません。テナント管理ページで特定テナントに設定してください。',
 type: '型',
 currentValue: '現在値',
 defaultValue: 'デフォルト値',
 options: '選択肢',
 dangerLevel: '危険レベル',
 hotReload: 'ホットリロード',
 noRequiresRestart: 'いいえ（再起動が必要）',
 observability: 'オブザーバビリティ',
 detectField: '識別フィールド {key}',
 emptyHint: '← 左側で設定を選択すると詳細が表示されます',
    errors: {
      loadListFailed: '設定一覧の読み込みに失敗しました。再試行してください。',
      loadDetailFailed: '設定詳細の読み込みに失敗しました。再試行してください。',
      saveFailed: '設定の保存に失敗しました。再試行してください。',
      rollbackFailed: 'ロールバックを完了できませんでした。再試行してください。',
      invalidNumber: '有効な数値を入力してください',
    },
  },
  editor: {
 newValue: '新しい値',
 aliasPlaceholder: '別名を入力して Enter',
 aliasHint: 'カンマ区切り文字列として保存され、セッション別名抽出に即時ホット更新されます。',
 aliasPreviewTitle: 'リクエスト例プレビュー',
 stringPlaceholder: '文字列値を入力',
 jsonPlaceholder: 'JSON形式で値を入力',
 jsonHint: '複合型は JSON形式で入力してください',
 rollback: 'ロールバック',
    rollbackConfirm: '{key} を前回の値にロールバックしますか？',

    enabledText: '有効',
    disabledText: '無効',
    saving: '保存中…',
    save: '保存',
  },
  compression: {
    enumLabels: {
      off: '0 - オフ (off)',
      auto: '1 - 自動しきい値 (auto_threshold)',
      on4xx: '2 - 4xx 時に圧縮 (on_4xx)',
      smart: 'スマート圧縮 (smart)',
      aggressive: 'アグレッシブ圧縮 (aggressive)',
    },
    enumDescriptions: {
      off: 'メッセージ圧縮機能を完全に無効化',
      auto: 'メッセージ長がコンテキストウィンドウの閾値を超えた場合に自動圧縮',
      on4xx: '4xx エラー (context_length_exceeded など) を受信した場合に圧縮して再試行',
    },
    strategyEnum: {
      naive: 'naive - 単純圧縮',
      smart: 'smart - スマート圧縮',
      adaptive: 'adaptive - 適応圧縮',
    },
  },
 gateway: {
 modeEnum: {
 simple: 'simple - シンプルモード',
 expert: 'expert - エキスパートモード',
 },
 recommended: '（推奨）',
 modeDescriptions: {
 simple: 'シンプルモード：ホームは接続リストのみ、エキスパート機能は「詳細」グループにまとめられています',
 expert: 'エキスパートモード：完全なナビゲーションと全ページ（現在のデフォルト）',
 },
 },
 timezone: {
    hint: 'タイムゾーンはすべての時間範囲の集計口径と表示（日替わり、レポート、ログ検索）を決定します。既定は UTC+8 で、保存後すぐに反映されます。',
    currentNow: '現在のタイムゾーンオフセット：{offset}',
  },
  search: {
    placeholder: '設定を検索：キー / 説明 / カテゴリ…',
  },
  basicConfig: {
    toggle: '基本設定',
    hotReload: 'ホット反映',
    group: {
      identity: '識別と外観',
      storage: 'ストレージ',
      schedules: '定期タスク',
    },
    entry: {
      displayName: 'ゲートウェイ表示名',
      skin: '既定のスキン',
      timezone: 'システムタイムゾーン',
      gatewayMode: '動作モード',
      attachmentUrl: '添付ファイル公開 URL',
      attachmentDir: '添付ファイル保存ディレクトリ',
      rollupHour: '集計時刻（UTC）',
    },
    skin: {
      auto: 'システムに従う',
      light: 'ライト',
      dark: 'ダーク',
    },
    infraTitle: 'インフラ情報（読み取り専用）',
    infraHint: '以下は起動時の環境設定による稼働状況です。変更にはサービス再起動が必要です。認証情報はこのページに表示されません。',
    infraNotConfigured: '未設定',
    infraNotSet: '未設定',
    infra: {
      instance: 'インスタンス ID',
      redis: 'Redis キャッシュ',
      database: 'データベース',
      cacheDir: 'キャッシュディレクトリ（env）',
      attachmentDir: '添付ディレクトリ（env）',
    },
  },
  docs: {
    compressionModeTitle: '📖 圧縮モード詳解',
    compressionModeContent: `<p><strong>圧縮モード</strong>は、過剰な会話コンテキストをシステムが処理する方法を制御します:</p>
<ul>
  <li><code>0 (off)</code> - 圧縮無効、コンテキスト超過時にエラーを直接返す</li>
  <li><code>1 (auto_threshold)</code> - 予兆モード、メッセージ長がモデルの context window に近づいたときに能動的に圧縮</li>
  <li><code>2 (on_4xx)</code> - リアクティブモード、4xx エラーを受信後に圧縮して再試行【推奨】</li>
</ul>
<p class="docs-note">💡 <strong>モード 2 を推奨</strong>:必要な場合にのみ圧縮することで、不要なパフォーマンスオーバーヘッドを回避</p>`,
    cacheEnabledTitle: '📖 セッションキャッシュ詳解',
    cacheEnabledContent: `<p><strong>セッションキャッシュ</strong>は、L1/L2/L3 の三段階キャッシュを有効化するかどうかを制御します:</p>
<ul>
  <li><strong>L1</strong> - メモリキャッシュ（最速）</li>
  <li><strong>L2</strong> - Redis キャッシュ（中程度）</li>
  <li><strong>L3</strong> - データベースキャッシュ（最も遅い）</li>
</ul>
<p class="docs-note">⚠️ 無効にすると、すべてのセッション状態が保存されず、コンテキストの連続性に影響します</p>`,
    formatConversionTitle: '📖 フォーマット変換詳解',
    formatConversionContent: `<p><strong>フォーマット変換</strong>は、異なるプロトコル間のリクエストフォーマットを自動変換することを可能にします:</p>
<ul>
  <li><strong>Q2 パス</strong>:Anthropic フォーマット → OpenAI モデル</li>
  <li><strong>Q3 パス</strong>:OpenAI フォーマット → Anthropic モデル</li>
</ul>
<p class="docs-note">💡 Provider レベルのオーバーライドをサポート、特定のプロバイダーで変換を無効化可能</p>`,
    rateLimitRpmTitle: '📖 RPM 制限詳解',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> は、各テナントの分間リクエスト数を制限します:</p>
<ul>
  <li>粗粒度のトラフィック制御に適しています</li>
  <li>スライディングウィンドウアルゴリズムに基づき、秒単位で正確</li>
  <li>超過時は 429 ステータスコードを返します</li>
</ul>
<p class="docs-note">⚠️ <strong>テナントレベル設定</strong>:この設定には tenant_id の指定が必要で、テナント管理ページで設定します</p>`,
    rateLimitConcurrentTitle: '📖 同時実行制限詳解',
    rateLimitConcurrentContent: `<p><strong>同時実行制限</strong>は、各テナントが同時に処理するリクエスト数を制限します:</p>
<ul>
  <li>システムリソースの保護、単一テナントが過度に接続を占有することを防ぐのに適しています</li>
  <li>カウンター実装に基づき、レスポンスが高速</li>
  <li>超過時はキューに入れるか 429 を返します</li>
</ul>
<p class="docs-note">⚠️ <strong>テナントレベル設定</strong>:この設定には tenant_id の指定が必要で、テナント管理ページで設定します</p>`,
  },

  // 同步的扁平键
}
