/**
 * Stage Tree Types — Frontend data structures for request pipeline visualization.
 * 
 * These types mirror the backend StageTreeFrame and related structures defined
 * in admin/live_stage_tree_projection.go. They provide type-safe access to the
 * complete request pipeline state pushed via SSE.
 * 
 * Design principles:
 *  1. Wire-compatible: field names match the JSON serialization exactly
 *  2. Comprehensive: all pipeline stages from auth to reply
 *  3. Progressive: status updates as execution progresses
 *  4. Safe: no sensitive data (credentials sanitized on backend)
 * 
 * @module types/stageTree
 * @since 2026-11-16
 */

/**
 * Stage execution status.
 */
export type StageStatus = 
  | 'pending'   // Not yet reached
  | 'active'    // Currently executing
  | 'done'      // Completed successfully
  | 'failed'    // Failed with error
  | 'skipped'   // Intentionally bypassed
  | 'warning'   // Completed with warnings
  | 'unknown';  // Status not measured

/**
 * Pipeline stage identifier.
 */
export type StageKey = 
  // Send rail (left to right)
  | 'auth'       // Authentication & rate limiting
  | 'approval'   // Human approval gate
  | 'injection'  // Prompt injection detection
  | 'sanitize'   // Data sanitization/masking
  | 'compress'   // Session compression
  | 'route'      // Credential routing
  | 'provider'   // Upstream provider/pool
  // Receive rail (right to left)
  | 'first_byte' // Time to first token
  | 'restore'    // Data restoration
  | 'billing'    // Token metering
  | 'reply';     // Response delivery

/**
 * Routing phase (from RouteTrace).
 */
export type RoutePhase = 
  | 'started'    // Request received, routing begins
  | 'selecting'  // Choosing credential/provider
  | 'attempting' // Sending request to upstream
  | 'responding' // Receiving response content
  | 'fallback'   // Retrying with next credential
  | 'done'       // Request completed successfully
  | 'failed';    // All attempts exhausted

/**
 * Error source classification.
 */
export type ErrorSource = 'gateway' | 'provider' | 'other';

/**
 * Individual pipeline stage node.
 */
export interface StageNode {
  key: StageKey;
  name: string;              // Display name (localized)
  status: StageStatus;
  start_time?: string;       // ISO 8601 timestamp
  end_time?: string;         // ISO 8601 timestamp
  duration_ms?: number;
  
  // Stage-specific metadata (varies by stage)
  metadata?: Record<string, any>;
  
  // Error details (when status = failed/warning)
  error_kind?: string;
  error_message?: string;
  error_source?: ErrorSource;
}

/**
 * Single upstream request attempt.
 */
export interface RouteAttempt {
  sequence: number;           // 1, 2, 3, ...
  credential_id: number;
  credential_label?: string;
  provider: string;
  model: string;
  
  start_time: string;         // ISO 8601
  end_time?: string;          // ISO 8601
  
  status?: number;            // HTTP status code
  success: boolean;
  latency_ms?: number;
  ttft_ms?: number;           // Time to first token
  
  error_kind?: string;
  error_message?: string;
  is_fallback: boolean;       // True if this is a retry
  
  // Token usage for this attempt
  prompt_tokens?: number;
  completion_tokens?: number;
}

/**
 * Complete stage tree visualization frame for a request.
 * This is the main data structure pushed via SSE for pipeline visualization.
 */
export interface StageTreeFrame {
  request_id: string;
  tenant_id: number;
  
  // Client info
  client_name?: string;
  client_type?: string;
  
  // Model resolution
  client_model: string;
  canonical_model?: string;
  is_auto_model?: boolean;
  
  // Current state
  phase: RoutePhase;
  overall_status: StageStatus;
  
  // Pipeline stages
  send_stages: StageNode[];    // Auth → Provider (7 stages)
  recv_stages: StageNode[];    // FirstByte → Reply (4 stages)
  
  // Routing attempts
  attempts?: RouteAttempt[];
  current_attempt: number;
  
  // Aggregated metrics
  total_latency_ms?: number;
  ttft_ms?: number;            // Time to first token
  tokens_in?: number;
  tokens_out?: number;
  cache_read?: number;
  
  // Session context
  session_id?: string;
  session_turn?: number;
  is_first_turn?: boolean;
  
  // Timestamps
  start_time: string;          // ISO 8601
  updated_at: string;          // ISO 8601
  version: number;             // Increments on each update
}

/**
 * Stage display names (localized).
 */
export const STAGE_NAMES: Record<StageKey, string> = {
  auth: '鉴权/限流',
  approval: '审批',
  injection: '注入检测',
  sanitize: '脱敏',
  compress: '压缩',
  route: '路由',
  provider: '供应商/令牌池',
  first_byte: '首字节',
  restore: '还原',
  billing: '计量',
  reply: '回复',
};

/**
 * Stage status display names (localized).
 */
export const STATUS_NAMES: Record<StageStatus, string> = {
  pending: '未到达',
  active: '进行中',
  done: '完成',
  failed: '失败',
  skipped: '跳过',
  warning: '告警',
  unknown: '未测量',
};

/**
 * Phase display names (localized).
 */
export const PHASE_NAMES: Record<RoutePhase, string> = {
  started: '已启动',
  selecting: '选择路由',
  attempting: '请求上游',
  responding: '接收响应',
  fallback: '重试中',
  done: '已完成',
  failed: '已失败',
};

/**
 * Error source display names (localized).
 */
export const ERROR_SOURCE_NAMES: Record<ErrorSource, string> = {
  gateway: '网关',
  provider: '供应商',
  other: '其它',
};

/**
 * Status glyphs for compact mode (from design prototype).
 */
export const STATUS_GLYPHS: Record<StageStatus, string> = {
  done: '✓',
  active: '◐',
  pending: '○',
  skipped: '⌀',
  failed: '✕',
  warning: '⚠',
  unknown: '⋯',
};

/**
 * Compact mode glyphs (done/pending use color only, no glyph).
 */
export const COMPACT_GLYPHS: Record<StageStatus, string> = {
  done: '',
  active: '◐',
  pending: '',
  skipped: '⌀',
  failed: '✕',
  warning: '⚠',
  unknown: '',
};

/**
 * Stage icons (SVG paths, 24×24 viewBox).
 * These match ICON from the design prototype.
 */
export const STAGE_ICONS: Record<StageKey | 'client', string> = {
  client: '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>',
  auth: '<svg viewBox="0 0 24 24"><circle cx="8" cy="15" r="3.4"/><path d="M10.6 12.4 20 3M16.6 3H20v3.4"/></svg>',
  approval: '<svg viewBox="0 0 24 24"><path d="M12 3.2 20 6v6.1c0 4.6-3.3 7.8-8 9-4.7-1.2-8-4.4-8-9V6z"/><path d="m8.8 12.1 2.3 2.3 4.3-4.5"/></svg>',
  injection: '<svg viewBox="0 0 24 24"><path d="M12 3.2 20 6v6.1c0 4.6-3.3 7.8-8 9-4.7-1.2-8-4.4-8-9V6z"/><path d="M12 8.3v4.2"/><path d="M12 15.6h.01"/></svg>',
  sanitize: '<svg viewBox="0 0 24 24"><path d="M3.5 20.5 13 11"/><path d="m17.2 3.4 1.1 2.4 2.4 1.1-2.4 1.1-1.1 2.4-1.1-2.4-2.4-1.1 2.4-1.1z"/><path d="m6.2 17.8 2 2"/></svg>',
  compress: '<svg viewBox="0 0 24 24"><path d="M9.5 4.5 12 7l2.5-2.5"/><path d="M9.5 19.5 12 17l2.5 2.5"/><path d="M12 7v10"/></svg>',
  route: '<svg viewBox="0 0 24 24"><circle cx="5.5" cy="6" r="2.2"/><circle cx="18.5" cy="6" r="2.2"/><circle cx="12" cy="18.5" r="2.2"/><path d="M7.7 6h8.6"/><path d="M12 7.8v8.5"/></svg>',
  provider: '<svg viewBox="0 0 24 24"><rect x="3.5" y="4" width="17" height="6" rx="1.6"/><rect x="3.5" y="14" width="17" height="6" rx="1.6"/><path d="M7 7h.01M7 17h.01"/></svg>',
  first_byte: '<svg viewBox="0 0 24 24"><path d="M13.2 2.5 4.8 13.4h5.6L10 21.5l9-11.4h-6z"/></svg>',
  restore: '<svg viewBox="0 0 24 24"><path d="M4.5 9.5h10.8a4.7 4.7 0 1 1 0 9.4H9.6"/><path d="m8.4 5.4-3.9 4.1 3.9 4.1"/></svg>',
  billing: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.2"/><path d="M9.4 8.6c0-1 1.2-1.8 2.6-1.8s2.6.8 2.6 1.8-1.2 1.4-2.6 1.6-2.6.6-2.6 1.6 1.2 1.8 2.6 1.8 2.6-.8 2.6-1.8"/><path d="M12 6.2v11.6"/></svg>',
  reply: '<svg viewBox="0 0 24 24"><path d="M20 14.2a2.8 2.8 0 0 1-2.8 2.8H8.4L4 20.5V6.2A2.8 2.8 0 0 1 6.8 3.4h10.4A2.8 2.8 0 0 1 20 6.2z"/><path d="M8.6 8.6h6.8M8.6 12h4.6"/></svg>',
};

/**
 * Helper: Find a stage by key in a stage list.
 */
export function findStage(stages: StageNode[], key: StageKey): StageNode | undefined {
  return stages.find(s => s.key === key);
}

/**
 * Helper: Check if request is in terminal state (done or failed).
 */
export function isTerminal(phase: RoutePhase): boolean {
  return phase === 'done' || phase === 'failed';
}

/**
 * Helper: Format duration for display.
 */
export function formatDuration(ms: number | undefined): string {
  if (ms == null) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const r = Math.round(s % 60);
  return `${m}m${r ? r + 's' : ''}`;
}

/**
 * Helper: Format token count for display (compact: 1.2k).
 */
export function formatTokens(n: number | undefined): string {
  if (n == null) return '—';
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

/**
 * Helper: Get stage color based on status.
 */
export function getStageColor(status: StageStatus): string {
  switch (status) {
    case 'done': return 'var(--kx-success)';
    case 'active': return 'var(--kx-primary)';
    case 'failed': return 'var(--kx-danger)';
    case 'warning': return 'var(--kx-warning)';
    case 'pending': return 'var(--kx-muted)';
    case 'skipped': return 'var(--kx-muted)';
    default: return 'var(--kx-muted)';
  }
}
