// useLiveStream — thin Vue composable that subscribes to the
// singleton SSE store (see liveStreamStore.ts).

import { onBeforeUnmount } from 'vue'
import {
  acquireLiveStream,
  pauseStream,
  resumeStream,
  togglePause,
  resetStream,
  reconnectStream,
  setOnRequestEvicted,
  requestsRef,
  snapshotRef,
  connectionRef,
  pausedRef,
  lastEventAtRef,
  type LiveRequest,
  type ConnectionState,
} from './liveStreamStore'

export {
  type LiveRequest,
  type LiveStatus,
  type LiveModelCategory,
  type LiveStreamEnvelope,
  type LiveStreamDelta,
  type ConnectionState,
} from './liveStreamStore'

export function useLiveStream() {
  const release = acquireLiveStream()
  // 2026-09-01 (P1 audit fix): onRequestEvicted now returns an unregister
  // function; use it as the composable's eviction listener cleanup hook so
  // each consumer only removes its own callback (no singleton overwrite).
  let evictUnregister: (() => void) | null = null

  onBeforeUnmount(() => {
    release()
    evictUnregister?.()
  })

  return {
    requests: requestsRef,
    snapshot: snapshotRef,
    connection: connectionRef,
    paused: pausedRef,
    lastEventAt: lastEventAtRef,
    pause: pauseStream,
    resume: resumeStream,
    togglePause,
    reset: resetStream,
    reconnect: reconnectStream,
    onRequestEvicted: (cb: ((id: string) => void) | null) => {
      // Each call returns a fresh unregister; dispose any previous one first
      // so consumers can call onRequestEvicted multiple times safely.
      evictUnregister?.()
      evictUnregister = setOnRequestEvicted(cb)
    },
  }
}
