// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — SettingsView 文案。命名空間：category / list / detail / editor / docs / errors。
// 技術欄位名（JSON / RPM / TPM / WebSocket 等）保持不譯。
export default {
  category: {
    all: '全部',
    compression: '壓縮',
    rateLimit: '限流',
    timeout: '逾時',
    routing: '路由',
    session: '會話',
    security: '安全',
    circuitBreaker: '熔斷',
    general: '其他',
  },
  dangerLevel: {
    note: '🟡 注意',
    warn: '🟠 警告',
    danger: '🔴 危險',
  },
  list: {
    total: '共 {n} 個設定',
    loading: '載入中…',
    empty: '該類別暫無設定',
    table: {
      setting: '設定',
      currentValue: '目前值',
      source: '來源',
      danger: '危險',
    },
  },
  detail: {
    selectPrompt: '← 從左側選擇一個設定查看詳情',
    type: '類型',
    currentValue: '目前值',
    defaultValue: '預設值',
    options: '選項',
    dangerLevel: '危險級別',
    hotReload: '熱重載',
    hotReloadYes: '是',
    hotReloadNo: '否（需重啟）',
    observability: '觀察點',
    tenantWarningTitle: '租戶級設定',
    tenantWarningBody: '此設定作用於單一租戶，無法在系統級設定。請前往<strong>租戶管理</strong>頁面為特定租戶設定此項。',
    errors: {
      loadListFailed: '載入失敗',
      loadDetailFailed: '載入詳情失敗',
      saveFailed: '儲存失敗',
      rollbackFailed: '回復失敗',
      invalidNumber: '請輸入有效的數字',
      confirmRollback: '確認回復 {key} 到上次的值？',
    },
  },
  editor: {
    newValueLabel: '新值',
    enabledText: '啟用',
    disabledText: '停用',
    stringPlaceholder: '輸入字串值',
    jsonPlaceholder: '輸入JSON格式的值',
    jsonHint: '複雜類型請使用JSON格式',
    saving: '儲存中…',
    save: '儲存',
    rollback: '回復',
  },
  compression: {
    selectLabels: {
      off: '0 - 關閉 (off)',
      auto: '1 - 自動閾值 (auto_threshold)',
      on4xx: '2 - 4xx時壓縮 (on_4xx) 【推薦】',
      deltaOnly: '僅差異 (delta_only)',
      smart: '智慧壓縮 (smart) 【預設】',
      aggressive: '激進壓縮 (aggressive)',
      headroom: 'Headroom 壓縮 (headroom)',
      headroomAggressive: 'Headroom 激進 (headroom_aggressive)',
    },
    enumLabels: {
      off: '0 - 關閉 (off)',
      auto: '1 - 自動閾值 (auto_threshold)',
      on4xx: '2 - 4xx時壓縮 (on_4xx)',
      deltaOnly: '僅差異 (delta_only)',
      smart: '智慧壓縮 (smart)',
      aggressive: '激進壓縮 (aggressive)',
      headroom: 'Headroom 壓縮 (headroom)',
      headroomAggressive: 'Headroom 激進 (headroom_aggressive)',
    },
    enumDescriptions: {
      off: '完全關閉訊息壓縮功能',
      auto: '當訊息長度超過context window閾值時自動壓縮',
      on4xx: '收到4xx錯誤（如context_length_exceeded）時觸發壓縮並重試',
      deltaOnly: '僅保留訊息增量，不進行會話壓縮',
      smart: 'v4 智慧壓縮：工具裁剪 + 滑動視窗',
      aggressive: 'v4 激進壓縮：任務分析 + 主動壓縮',
      headroom: 'v7 Headroom 壓縮：智慧 JSON 陣列壓縮',
      headroomAggressive: 'v7 Headroom 激進：更激進的陣列壓縮 + CCR 儲存',
    },
    hint: {
      off: '完全關閉訊息壓縮功能',
      auto: '當訊息長度超過context window閾值時自動壓縮',
      on4xx: '收到4xx錯誤（如context_length_exceeded）時觸發壓縮並重試',
      deltaOnly: '僅保留訊息增量，不進行會話壓縮（適用於無狀態場景）',
      smart: 'v4 智慧壓縮：包含工具定義裁剪和滑動視窗最佳化（預設推薦）',
      aggressive: 'v4 激進壓縮：包含任務分析和主動壓縮策略（高壓縮比）',
      headroom: 'v7 Headroom 壓縮：智慧壓縮 JSON 陣列（如大量結構化資料），保持語意完整性',
      headroomAggressive: 'v7 Headroom 激進模式：更激進的陣列壓縮演算法 + CCR 三級快取儲存（最高壓縮比）',
    },
    strategyEnum: {
      naive: 'naive - 樸素壓縮',
      smart: 'smart - 智慧壓縮',
      adaptive: 'adaptive - 自適應壓縮',
    },
  },
  docs: {
    compressionModeTitle: '📖 壓縮模式詳解',
    compressionModeContent: `<p><strong>壓縮模式</strong>控制系統如何處理超長對話上下文：</p>
<ul>
  <li><code>0 (off)</code> - 關閉壓縮，當上下文超限時直接返回錯誤</li>
  <li><code>1 (auto_threshold)</code> - 預判模式，當訊息長度接近模型的context window時主動壓縮</li>
  <li><code>2 (on_4xx)</code> - 回應式模式，收到4xx錯誤後壓縮並重試【推薦】</li>
</ul>
<p class="docs-note">💡 <strong>推薦使用模式2</strong>：僅在必要時壓縮，避免不必要的效能開銷</p>`,
    cacheEnabledTitle: '📖 會話快取詳解',
    cacheEnabledContent: `<p><strong>會話快取</strong>控制是否啟用L1/L2/L3三級快取：</p>
<ul>
  <li><strong>L1</strong> - 記憶體快取（最快）</li>
  <li><strong>L2</strong> - Redis快取（中等）</li>
  <li><strong>L3</strong> - 資料庫快取（最慢）</li>
</ul>
<p class="docs-note">⚠️ 關閉後所有會話狀態將不被儲存，影響上下文連續性</p>`,
    formatConversionTitle: '📖 格式轉換詳解',
    formatConversionContent: `<p><strong>格式轉換</strong>允許不同協定之間的請求格式自動轉換：</p>
<ul>
  <li><strong>Q2路徑</strong>：Anthropic格式 → OpenAI模型</li>
  <li><strong>Q3路徑</strong>：OpenAI格式 → Anthropic模型</li>
</ul>
<p class="docs-note">💡 支援Provider級別覆寫，可針對特定供應商停用轉換</p>`,
    rateLimitRpmTitle: '📖 RPM限流詳解',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> 限制每個租戶每分鐘的請求次數：</p>
<ul>
  <li>適用於粗粒度的流量控制</li>
  <li>基於滑動視窗演算法，精確到秒級</li>
  <li>超限後返回429狀態碼</li>
</ul>
<p class="docs-note">⚠️ <strong>租戶級設定</strong>：此設定需要指定tenant_id，在租戶管理頁面設定</p>`,
    rateLimitConcurrentTitle: '📖 並行限流詳解',
    rateLimitConcurrentContent: `<p><strong>並行限流</strong>限制每個租戶同時處理的請求數量：</p>
<ul>
  <li>適用於保護系統資源，防止單一租戶佔用過多連線</li>
  <li>基於計數器實作，回應速度快</li>
  <li>超限後排隊或返回429</li>
</ul>
<p class="docs-note">⚠️ <strong>租戶級設定</strong>：此設定需要指定tenant_id，在租戶管理頁面設定</p>`,
  },
}
