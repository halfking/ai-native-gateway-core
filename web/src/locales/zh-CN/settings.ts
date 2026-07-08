// settings.ts — SettingsView 文案。命名空间：category / list / detail / editor / docs / errors。
// 技术字段名（JSON / RPM / TPM / WebSocket 等）保持不译。
export default {
  category: {
    all: '全部',
    compression: '压缩',
    rateLimit: '限流',
    timeout: '超时',
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
  list: {
    total: '共 {n} 个设置',
    loading: '加载中…',
    empty: '该类别暂无设置',
    table: {
      setting: '设置',
      currentValue: '当前值',
      source: '来源',
      danger: '危险',
    },
  },
  detail: {
    selectPrompt: '← 从左侧选择一个设置查看详情',
    type: '类型',
    currentValue: '当前值',
    defaultValue: '默认值',
    options: '选项',
    dangerLevel: '危险级别',
    hotReload: '热重载',
    hotReloadYes: '是',
    hotReloadNo: '否（需重启）',
    observability: '观察点',
    tenantWarningTitle: '租户级配置',
    tenantWarningBody: '此设置作用于单个租户，无法在系统级设置。请前往<strong>租户管理</strong>页面为特定租户配置此项。',
    errors: {
      loadListFailed: '加载失败',
      loadDetailFailed: '加载详情失败',
      saveFailed: '保存失败',
      rollbackFailed: '回滚失败',
      invalidNumber: '请输入有效的数字',
      confirmRollback: '确认回滚 {key} 到上次的值？',
    },
  },
  editor: {
    newValueLabel: '新值',
    enabledText: '启用',
    disabledText: '禁用',
    stringPlaceholder: '输入字符串值',
    jsonPlaceholder: '输入JSON格式的值',
    jsonHint: '复杂类型请使用JSON格式',
    saving: '保存中…',
    save: '保存',
    rollback: '回滚',
  },
  compression: {
    selectLabels: {
      off: '关闭 (off)',
      auto: '自动阈值 (auto_threshold)',
      on4xx: '4xx时压缩 (on_4xx)',
      deltaOnly: '仅增量 (delta_only)',
      smart: '智能压缩 (smart) 【默认】',
      aggressive: '激进压缩 (aggressive)',
      headroom: 'Headroom 压缩 (headroom)',
      headroomAggressive: 'Headroom 激进 (headroom_aggressive)',
    },
    enumLabels: {
      off: '关闭 (off)',
      auto: '自动阈值 (auto_threshold)',
      on4xx: '4xx时压缩 (on_4xx)',
      deltaOnly: '仅增量 (delta_only)',
      smart: '智能压缩 (smart)',
      aggressive: '激进压缩 (aggressive)',
      headroom: 'Headroom 压缩 (headroom)',
      headroomAggressive: 'Headroom 激进 (headroom_aggressive)',
    },
    enumDescriptions: {
      off: '完全关闭消息压缩功能',
      auto: '当消息长度超过context window阈值时自动压缩',
      on4xx: '收到4xx错误（如context_length_exceeded）时触发压缩并重试',
      deltaOnly: '仅保留消息增量，不进行会话压缩',
      smart: 'v4 智能压缩：工具裁剪 + 滑动窗口',
      aggressive: 'v4 激进压缩：任务分析 + 主动压缩',
      headroom: 'v7 Headroom 压缩：智能 JSON 数组压缩',
      headroomAggressive: 'v7 Headroom 激进：更激进的数组压缩 + CCR 存储',
    },
    hint: {
      off: '完全关闭消息压缩功能',
      auto: '当消息长度超过context window阈值时自动压缩',
      on4xx: '收到4xx错误（如context_length_exceeded）时触发压缩并重试',
      deltaOnly: '仅保留消息增量，不进行会话压缩（适用于无状态场景）',
      smart: 'v4 智能压缩：包含工具定义裁剪和滑动窗口优化（默认推荐）',
      aggressive: 'v4 激进压缩：包含任务分析和主动压缩策略（高压缩比）',
      headroom: 'v7 Headroom 压缩：智能压缩 JSON 数组（如大量结构化数据），保持语义完整性',
      headroomAggressive: 'v7 Headroom 激进模式：更激进的数组压缩算法 + CCR 三级缓存存储（最高压缩比）',
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
