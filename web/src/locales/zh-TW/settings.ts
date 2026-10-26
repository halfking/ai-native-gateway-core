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
    retry: '重試與逾時',
    routing: '路由',
    session: '會話',
    security: '安全',
    outputCompliance: '輸出合規',
    circuitBreaker: '熔斷',
    general: '其他',
  },
  dangerLevel: {
    note: '🟡 注意',
    warn: '🟠 警告',
    danger: '🔴 危險',
  },
  detail: {
    errors: {
      loadListFailed: '設定列表沒能載入出來，請重試',
      loadDetailFailed: '設定詳情沒能載入出來，請重試',
      saveFailed: '設定沒能儲存，請重試',
      rollbackFailed: '回滾沒能完成，請重試',
      invalidNumber: '請輸入有效的數字',
    },
  },
  editor: {
    rollbackConfirm: '確認回滾 {key} 到上次的值？',

    enabledText: '啟用',
    disabledText: '停用',
    saving: '儲存中…',
    save: '儲存',
  },
  compression: {
    enumLabels: {
      off: '0 - 關閉 (off)',
      auto: '1 - 自動閾值 (auto_threshold)',
      on4xx: '2 - 4xx時壓縮 (on_4xx)',
      smart: '智慧壓縮 (smart)',
      aggressive: '激進壓縮 (aggressive)',
    },
    enumDescriptions: {
      off: '完全關閉訊息壓縮功能',
      auto: '當訊息長度超過context window閾值時自動壓縮',
      on4xx: '收到4xx錯誤（如context_length_exceeded）時觸發壓縮並重試',
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

  // 同步的扁平键
}
