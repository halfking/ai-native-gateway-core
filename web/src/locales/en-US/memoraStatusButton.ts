// English locale for MemoraStatusButton
export default {
  state: {
    disabled: 'Disabled',
    paused: 'Disconnected',
    ok: 'Connected',
    error: 'Connection Failed',
    loading: 'Checking',
  },
  
  panel: {
    title: 'Memora Connection',
    closeLabel: 'Close',
    
    fields: {
      serviceUrl: 'Service URL',
      recentLatency: 'Recent Latency',
      error: 'Error',
      check: 'Check',
      writeQueue: 'Write Queue',
      processedErrored: 'Processed / Errored',
      consecutiveErrors: 'Consecutive Errors',
      recentWriteError: 'Recent Write Error',
    },
    
    actions: {
      processing: 'Processing…',
      reconnect: 'Reconnect',
      checkConnectivity: 'Check Connectivity',
      disconnect: 'Disconnect Write',
      refresh: 'Refresh',
      sessionContext: 'Session Context',
    },
  },
  
  error: {
    connectionFailed: 'Connection failed',
    checkFailed: 'Check failed',
  },
}
