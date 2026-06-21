import { req } from './_core'
import type { UserInfo } from './_core'

// auth.ts — v6.0 audit T12 (2026-06-22)
// JWT login (/api/auth/token) and gateway session lifecycle
// (/v1/sessions). Note: createGatewaySession / deleteGatewaySession
// bypass `req<T>` because they use sk-* bearer auth, not the JWT
// stored in the Pinia store. The /v1/sessions endpoint also requires
// X-Device-Seed for sticky routing, so it can't go through the generic
// 401-redirect plumbing.

// ── Auth ──────────────────────────────────────────────────────────────────

export interface LoginResponse {
  access_token?: string
  token_type?: string
  expires_in?: number
  user?: UserInfo

  api_key: string
  key_prefix: string
  message: string
}

export function login(username: string, password: string) {
  return req<LoginResponse>('POST', '/api/auth/token', { username, password })
}

// ── Gateway sessions (OpenAI-compatible /v1/sessions) ───────────────────

export interface GatewaySessionCreated {
  session_id: string
  session_key: string
  expires_at: string
  created_at: string
}

/** Create a gw_ session for /v1/chat/completions (sk-* auth, not JWT). */
export async function createGatewaySession(
  apiKey: string,
  taskId?: string,
): Promise<GatewaySessionCreated> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${apiKey}`,
  }
  if (taskId) headers['X-Gw-Task-Id'] = taskId
  const deviceSeed = localStorage.getItem('llmgw_device_seed') ?? 'default'
  headers['X-Device-Seed'] = deviceSeed

  const r = await fetch('/v1/sessions', {
    method: 'POST',
    headers,
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
  if (!r.ok) {
    const text = await r.text()
    let msg = `HTTP ${r.status}`
    try {
      const j = JSON.parse(text)
      msg = j?.error?.message || text || msg
    } catch {
      msg = text || msg
    }
    throw new Error(msg)
  }
  return r.json()
}

/** Delete a gateway session (sk-* auth). Best-effort cleanup when removing a chat. */
export async function deleteGatewaySession(apiKey: string, sessionId: string): Promise<void> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${apiKey}`,
  }
  const deviceSeed = localStorage.getItem('llmgw_device_seed') ?? 'default'
  headers['X-Device-Seed'] = deviceSeed

  const r = await fetch(`/v1/sessions/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE',
    headers,
  })
  if (!r.ok && r.status !== 404) {
    const text = await r.text()
    let msg = `HTTP ${r.status}`
    try {
      const j = JSON.parse(text)
      msg = j?.error?.message || text || msg
    } catch {
      msg = text || msg
    }
    throw new Error(msg)
  }
}