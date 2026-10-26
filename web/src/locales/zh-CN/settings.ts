// settings.ts — SettingsView 文案。命名空间：category / list / detail / editor / docs / errors。
// 技术字段名（JSON / RPM / TPM / WebSocket 等）保持不译。
export default {
  category: {
    all: '全部',
    compression: '压缩',
    rateLimit: '限流',
    timeout: '超时',
    retry: '重试与超时',
    routing: '路由',
    session: '会话',
    security: '安全',
    outputCompliance: '输出合规',
    circuitBreaker: '熔断',
    general: '其他',
  },
  dangerLevel: {
    note: '🟡 注意',
    warn: '🟠 警告',
    danger: '🔴 危险',
  },
  detail: {
    errors: {
      loadListFailed: '设置列表没能加载出来，请重试',
      loadDetailFailed: '设置详情没能加载出来，请重试',
      saveFailed: '设置没能保存，请重试',
      rollbackFailed: '回滚没能完成，请重试',
      invalidNumber: '请输入有效的数字',
    },
  },
  editor: {
    rollbackConfirm: '确认回滚 {key} 到上次的值？',

    enabledText: '启用',
    disabledText: '禁用',
    saving: '保存中…',
    save: '保存',
  },
  compression: {
    enumLabels: {
      off: '关闭 (off)',
      auto: '自动阈值 (auto_threshold)',
      on4xx: '4xx时压缩 (on_4xx)',
      smart: '智能压缩 (smart)',
      aggressive: '激进压缩 (aggressive)',
    },
    enumDescriptions: {
      off: '完全关闭消息压缩功能',
      auto: '当消息长度超过context window阈值时自动压缩',
      on4xx: '收到4xx错误（如context_length_exceeded）时触发压缩并重试',
    },
    strategyEnum: {
      naive: 'naive - 朴素压缩',
      smart: 'smart - 智能压缩',
      adaptive: 'adaptive - 自适应压缩',
    },
  },
  docs: {
    compressionModeTitle: '📖 压缩模式详解',
    compressionModeContent: `<p><strong>压缩模式</strong>控制系统如何处理超长对话上下文：</p>
<ul>
  <li><code>0 (off)</code> - 关闭压缩，当上下文超限时直接返回错误</li>
  <li><code>1 (auto_threshold)</code> - 预判模式，当消息长度接近模型的context window时主动压缩</li>
  <li><code>2 (on_4xx)</code> - 响应式模式，收到4xx错误后压缩并重试【推荐】</li>
</ul>
<p class="docs-note">💡 <strong>推荐使用模式2</strong>：仅在必要时压缩，避免不必要的性能开销</p>`,
    cacheEnabledTitle: '📖 会话缓存详解',
    cacheEnabledContent: `<p><strong>会话缓存</strong>控制是否启用L1/L2/L3三级缓存：</p>
<ul>
  <li><strong>L1</strong> - 内存缓存（最快）</li>
  <li><strong>L2</strong> - Redis缓存（中等）</li>
  <li><strong>L3</strong> - 数据库缓存（最慢）</li>
</ul>
<p class="docs-note">⚠️ 关闭后所有会话状态将不被保存，影响上下文连续性</p>`,
    formatConversionTitle: '📖 格式转换详解',
    formatConversionContent: `<p><strong>格式转换</strong>允许不同协议之间的请求格式自动转换：</p>
<ul>
  <li><strong>Q2路径</strong>：Anthropic格式 → OpenAI模型</li>
  <li><strong>Q3路径</strong>：OpenAI格式 → Anthropic模型</li>
</ul>
<p class="docs-note">💡 支持Provider级别覆盖，可针对特定供应商禁用转换</p>`,
    rateLimitRpmTitle: '📖 RPM限流详解',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> 限制每个租户每分钟的请求次数：</p>
<ul>
  <li>适用于粗粒度的流量控制</li>
  <li>基于滑动窗口算法，精确到秒级</li>
  <li>超限后返回429状态码</li>
</ul>
<p class="docs-note">⚠️ <strong>租户级配置</strong>：此设置需要指定tenant_id，在租户管理页面设置</p>`,
    rateLimitConcurrentTitle: '📖 并发限流详解',
    rateLimitConcurrentContent: `<p><strong>并发限流</strong>限制每个租户同时处理的请求数量：</p>
<ul>
  <li>适用于保护系统资源，防止单个租户占用过多连接</li>
  <li>基于计数器实现，响应速度快</li>
  <li>超限后排队或返回429</li>
</ul>
<p class="docs-note">⚠️ <strong>租户级配置</strong>：此设置需要指定tenant_id，在租户管理页面设置</p>`,
  },
}
