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

  onBeforeUnmount(() => {
    release()
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
    onRequestEvicted: (cb: ((id: string) => void) | null) => setOnRequestEvicted(cb),
  }
}
