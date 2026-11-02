import { describe, expect, it } from 'vitest'
import { isIdleMarker, isNoTrafficErrorKind } from './liveStreamIdle'
import { errorKindBg, errorKindLabel, statusCategory } from '../composables/liveStreamDisplay'

describe('isIdleMarker', () => {
  it('recognises status idle, type idle_marker, and idle- request ids', () => {
    expect(isIdleMarker({ status: 'idle' })).toBe(true)
    expect(isIdleMarker({ type: 'idle_marker', request_id: 'x' })).toBe(true)
    expect(isIdleMarker({ requestId: 'idle-global-vendor-zyphra_zamba2-7b-instruct' })).toBe(true)
  })

  it('treats no_traffic error kinds as idle, not failure', () => {
    expect(isNoTrafficErrorKind('no_traffic_5min')).toBe(true)
    expect(isIdleMarker({ error_kind: 'no_traffic_1min', status: 'idle' })).toBe(true)
    expect(statusCategory('idle', 'no_traffic_5min')).toBe('success')
    expect(errorKindLabel('no_traffic_5min')).toBe('idle')
    expect(errorKindBg('no_traffic_5min')).toBe('transparent')
  })

  it('does not misclassify real failures', () => {
    expect(isIdleMarker({ status: 'failure', error_kind: 'upstream_timeout', request_id: 'abc' })).toBe(false)
    // ★★ 2026-11-02：这条断言原先写 'failure_timeout' ⇒ **与实现相反** ⇒ 门红。
    //   ★★ 证据：**后端有两种语义不同的 timeout**，★ 前端的分层是刻意的：
    //   ★  裸 "timeout"        ← middleware/logging_mw.go:191 ← ★ 网关侧 HTTP 408/504
    //   ★  "upstream_timeout"  ← internal/trace/builder.go:416 ← ★ 上游调用超时
    //   ★★ 而 liveStreamDisplay.statusCategory 的负向预查
    //   ★★ /(?<!upstream_)(?<!backend_)(?<!server_)timeout/ ★★ 正是把这两层分开：
    //   ★★ 网关侧 → failure_timeout；★★ 上游侧 → failure_5xx（与 upstream_5xx 同族）
    //   ★★ ⇒ ★ 本断言钉住的是「上游侧超时**不**落网关侧超时档」这层语义。
    expect(statusCategory('failure', 'upstream_timeout')).toBe('failure_5xx')
  })
})
