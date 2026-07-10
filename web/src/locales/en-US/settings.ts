// settings.ts — SettingsView copy. Namespace: category / list / detail / editor / docs / errors.
// Technical field names (JSON / RPM / TPM / WebSocket) remain untranslated.
export default {
  category: {
    all: 'All',
    compression: 'Compression',
    rateLimit: 'Rate Limit',
    timeout: 'Timeout',
    routing: 'Routing',
    session: 'Session',
    security: 'Security',
    outputCompliance: 'Output Compliance',
    circuitBreaker: 'Circuit Breaker',
    general: 'Other',
  },
  dangerLevel: {
    note: '🟡 Note',
    warn: '🟠 Warning',
    danger: '🔴 Danger',
  },
  list: {
    total: '{n} settings total',
    loading: 'Loading…',
    empty: 'No settings in this category',
    table: {
      setting: 'Setting',
      currentValue: 'Current',
      source: 'Source',
      danger: 'Danger',
    },
  },
  detail: {
    selectPrompt: '← Select a setting on the left to view details',
    type: 'Type',
    currentValue: 'Current',
    defaultValue: 'Default',
    options: 'Options',
    dangerLevel: 'Danger Level',
    hotReload: 'Hot Reload',
    hotReloadYes: 'Yes',
    hotReloadNo: 'No (requires restart)',
    observability: 'Observability',
    tenantWarningTitle: 'Tenant-scoped setting',
    tenantWarningBody: 'This setting applies to a single tenant and cannot be configured system-wide. Please go to the <strong>Tenant Management</strong> page to configure it for a specific tenant.',
    errors: {
      loadListFailed: 'Failed to load',
      loadDetailFailed: 'Failed to load details',
      saveFailed: 'Failed to save',
      rollbackFailed: 'Failed to rollback',
      invalidNumber: 'Please enter a valid number',
      confirmRollback: 'Confirm rolling back {key} to its previous value?',
    },
  },
  editor: {
    newValueLabel: 'New value',
    enabledText: 'Enabled',
    disabledText: 'Disabled',
    stringPlaceholder: 'Enter a string value',
    jsonPlaceholder: 'Enter a JSON value',
    jsonHint: 'Use JSON for complex types',
    saving: 'Saving…',
    save: 'Save',
    rollback: 'Rollback',
  },
  compression: {
    selectLabels: {
      off: 'Off',
      auto: 'Auto threshold',
      on4xx: 'On 4xx',
      deltaOnly: 'Delta only',
      smart: 'Smart (default)',
      aggressive: 'Aggressive',
      headroom: 'Headroom',
      headroomAggressive: 'Headroom aggressive',
    },
    enumLabels: {
      off: 'Off',
      auto: 'Auto threshold',
      on4xx: 'On 4xx',
      deltaOnly: 'Delta only',
      smart: 'Smart',
      aggressive: 'Aggressive',
      headroom: 'Headroom',
      headroomAggressive: 'Headroom aggressive',
    },
    enumDescriptions: {
      off: 'Compression is fully disabled.',
      auto: 'Compress automatically when message length exceeds context window threshold.',
      on4xx: 'Compress and retry when receiving a 4xx error (e.g. context_length_exceeded).',
      deltaOnly: 'Only preserve message delta, no session compression.',
      smart: 'v4 smart compression: tool trimming + sliding window.',
      aggressive: 'v4 aggressive compression: task analysis + proactive compression.',
      headroom: 'v7 Headroom compression: smart JSON array compression.',
      headroomAggressive: 'v7 Headroom aggressive: aggressive array compression + CCR storage.',
    },
    hint: {
      off: 'Compression is fully disabled.',
      auto: 'Compress automatically when message length exceeds context window threshold.',
      on4xx: 'Compress and retry when receiving a 4xx error (e.g. context_length_exceeded).',
      deltaOnly: 'Only preserve message delta, no session compression (suitable for stateless scenarios).',
      smart: 'v4 smart compression: includes tool definition trimming and sliding window optimization (recommended default).',
      aggressive: 'v4 aggressive compression: includes task analysis and proactive compression strategies (high compression ratio).',
      headroom: 'v7 Headroom compression: intelligently compresses JSON arrays (e.g., large structured data) while maintaining semantic integrity.',
      headroomAggressive: 'v7 Headroom aggressive mode: more aggressive array compression algorithm + CCR three-tier cache storage (highest compression ratio).',
    },
    strategyEnum: {
      naive: 'naive - Naive compression',
      smart: 'smart - Smart compression',
      adaptive: 'adaptive - Adaptive compression',
    },
  },
  docs: {
    compressionModeTitle: '📖 Compression mode in detail',
    compressionModeContent: `<p><strong>Compression mode</strong> controls how the system handles over-long conversation context:</p>
<ul>
  <li><code>0 (off)</code> - Disable compression; return an error when context overflows.</li>
  <li><code>1 (auto_threshold)</code> - Predictive mode; compress proactively when messages approach the context window.</li>
  <li><code>2 (on_4xx)</code> - Reactive mode; compress and retry after a 4xx error. [Recommended]</li>
</ul>
<p class="docs-note">💡 <strong>Mode 2 is recommended</strong>: compress only when necessary, avoiding unnecessary performance overhead.</p>`,
    cacheEnabledTitle: '📖 Session cache in detail',
    cacheEnabledContent: `<p><strong>Session cache</strong> controls whether the L1/L2/L3 tiered cache is enabled:</p>
<ul>
  <li><strong>L1</strong> - In-memory cache (fastest)</li>
  <li><strong>L2</strong> - Redis cache (medium)</li>
  <li><strong>L3</strong> - Database cache (slowest)</li>
</ul>
<p class="docs-note">⚠️ When disabled, no session state is saved; this affects context continuity.</p>`,
    formatConversionTitle: '📖 Format conversion in detail',
    formatConversionContent: `<p><strong>Format conversion</strong> allows automatic request format conversion between protocols:</p>
<ul>
  <li><strong>Q2 path</strong>: Anthropic format → OpenAI models</li>
  <li><strong>Q3 path</strong>: OpenAI format → Anthropic models</li>
</ul>
<p class="docs-note">💡 Provider-level overrides are supported; conversion can be disabled for specific providers.</p>`,
    rateLimitRpmTitle: '📖 RPM rate limit in detail',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> limits the number of requests per tenant per minute:</p>
<ul>
  <li>Suitable for coarse-grained traffic control</li>
  <li>Uses a sliding window algorithm, precise to the second</li>
  <li>Returns HTTP 429 when exceeded</li>
</ul>
<p class="docs-note">⚠️ <strong>Tenant-scoped</strong>: this setting requires a tenant_id; configure it on the Tenant Management page.</p>`,
    rateLimitConcurrentTitle: '📖 Concurrent rate limit in detail',
    rateLimitConcurrentContent: `<p><strong>Concurrent rate limit</strong> limits the number of concurrent requests processed per tenant:</p>
<ul>
  <li>Protects system resources; prevents a single tenant from monopolizing connections</li>
  <li>Counter-based; fast response</li>
  <li>Returns 429 or queues when exceeded</li>
</ul>
<p class="docs-note">⚠️ <strong>Tenant-scoped</strong>: this setting requires a tenant_id; configure it on the Tenant Management page.</p>`,
  },
}
