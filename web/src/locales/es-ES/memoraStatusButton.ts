// Spanish locale for MemoraStatusButton
export default {
  state: {
    disabled: 'Deshabilitado',
    paused: 'Desconectado',
    ok: 'Conectado',
    error: 'Conexión fallida',
    loading: 'Verificando',
  },
  
  panel: {
    title: 'Conexión Memora',
    closeLabel: 'Cerrar',
    
    fields: {
      serviceUrl: 'URL del servicio',
      recentLatency: 'Latencia reciente',
      error: 'Error',
      check: 'Verificación',
      writeQueue: 'Cola de escritura',
      processedErrored: 'Procesado / Con errores',
      consecutiveErrors: 'Errores consecutivos',
      recentWriteError: 'Error de escritura reciente',
    },
    
    actions: {
      processing: 'Procesando…',
      reconnect: 'Reconectar',
      checkConnectivity: 'Verificar conectividad',
      disconnect: 'Desconectar escritura',
      refresh: 'Actualizar',
      sessionContext: 'Contexto de sesión',
    },
  },
  
  error: {
    connectionFailed: 'Conexión fallida',
    checkFailed: 'Verificación fallida',
  },
}
