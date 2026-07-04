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
    routing: 'ルーティング',
    session: 'セッション',
    security: 'セキュリティ',
    circuitBreaker: 'サーキットブレーカー',
    general: 'その他',
  },
  dangerLevel: {
    note: '🟡 注意',
    warn: '🟠 警告',
    danger: '🔴 危険',
  },
  list: {
    total: '全 {n} 設定',
    loading: '読み込み中…',
    empty: 'このカテゴリーには設定がありません',
    table: {
      setting: '設定',
      currentValue: '現在の値',
      source: 'ソース',
      danger: '危険度',
    },
  },
  detail: {
    selectPrompt: '← 左側から設定を選択して詳細を表示',
    type: 'タイプ',
    currentValue: '現在の値',
    defaultValue: 'デフォルト値',
    options: 'オプション',
    dangerLevel: '危険レベル',
    hotReload: 'ホットリロード',
    hotReloadYes: 'はい',
    hotReloadNo: 'いいえ(再起動が必要)',
    observability: '観測ポイント',
    tenantWarningTitle: 'テナントレベル設定',
    tenantWarningBody: 'この設定は単一テナントに作用するため、システムレベルでは設定できません。<strong>テナント管理</strong>ページで特定のテナントに設定してください。',
    errors: {
      loadListFailed: '読み込み失敗',
      loadDetailFailed: '詳細読み込み失敗',
      saveFailed: '保存失敗',
      rollbackFailed: 'ロールバック失敗',
      invalidNumber: '有効な数値を入力してください',
      confirmRollback: '{key} を前回の値にロールバックしますか？',
    },
  },
  editor: {
    newValueLabel: '新しい値',
    enabledText: '有効',
    disabledText: '無効',
    stringPlaceholder: '文字列値を入力',
    jsonPlaceholder: 'JSON 形式の値を入力',
    jsonHint: '複雑な型は JSON 形式を使用してください',
    saving: '保存中…',
    save: '保存',
    rollback: 'ロールバック',
  },
  compression: {
    selectLabels: {
      off: '0 - オフ (off)',
      auto: '1 - 自動しきい値 (auto_threshold)',
      on4xx: '2 - 4xx 時に圧縮 (on_4xx) 【推奨】',
      deltaOnly: '差分のみ (delta_only)',
      smart: 'スマート圧縮 (smart) 【デフォルト】',
      aggressive: 'アグレッシブ圧縮 (aggressive)',
      headroom: 'ヘッドルーム圧縮 (headroom)',
      headroomAggressive: 'ヘッドルームアグレッシブ (headroom_aggressive)',
    },
    enumLabels: {
      off: '0 - オフ (off)',
      auto: '1 - 自動しきい値 (auto_threshold)',
      on4xx: '2 - 4xx 時に圧縮 (on_4xx)',
      deltaOnly: '差分のみ (delta_only)',
      smart: 'スマート圧縮 (smart)',
      aggressive: 'アグレッシブ圧縮 (aggressive)',
      headroom: 'ヘッドルーム圧縮 (headroom)',
      headroomAggressive: 'ヘッドルームアグレッシブ (headroom_aggressive)',
    },
    enumDescriptions: {
      off: 'メッセージ圧縮機能を完全に無効化',
      auto: 'メッセージ長がコンテキストウィンドウの閾値を超えた場合に自動圧縮',
      on4xx: '4xx エラー (context_length_exceeded など) を受信した場合に圧縮して再試行',
      deltaOnly: 'メッセージ差分のみ保持、セッション圧縮は行わない',
      smart: 'v4 スマート圧縮：ツールトリミング + スライディングウィンドウ',
      aggressive: 'v4 アグレッシブ圧縮：タスク分析 + プロアクティブ圧縮',
      headroom: 'v7 ヘッドルーム圧縮：スマート JSON 配列圧縮',
      headroomAggressive: 'v7 ヘッドルームアグレッシブ：よりアグレッシブな配列圧縮 + CCR ストレージ',
    },
    hint: {
      off: 'メッセージ圧縮機能を完全に無効化',
      auto: 'メッセージ長がコンテキストウィンドウの閾値を超えた場合に自動圧縮',
      on4xx: '4xx エラー (context_length_exceeded など) を受信した場合に圧縮して再試行',
      deltaOnly: 'メッセージ差分のみ保持、セッション圧縮は行わない（ステートレスシナリオ向け）',
      smart: 'v4 スマート圧縮：ツール定義トリミングとスライディングウィンドウ最適化を含む（推奨デフォルト）',
      aggressive: 'v4 アグレッシブ圧縮：タスク分析とプロアクティブ圧縮戦略を含む（高圧縮比）',
      headroom: 'v7 ヘッドルーム圧縮：JSON 配列をスマートに圧縮（大量の構造化データなど）、意味の整合性を維持',
      headroomAggressive: 'v7 ヘッドルームアグレッシブモード：よりアグレッシブな配列圧縮アルゴリズム + CCR 三階層キャッシュストレージ（最高圧縮比）',
    },
    strategyEnum: {
      naive: 'naive - 単純圧縮',
      smart: 'smart - スマート圧縮',
      adaptive: 'adaptive - 適応圧縮',
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
}
