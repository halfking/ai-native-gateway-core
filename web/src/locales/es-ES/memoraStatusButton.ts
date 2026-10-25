// Spanish locale for MemoraStatusButton
export default {
  state: {
    disabled: 'Deshabilitado',
    paused: 'Desconectado',
    ok: 'Conectado',
    error: 'No se pudo conectar. Compruebe la red y vuelva a intentarlo.',
    loading: 'Verificando',
    detectFailed: 'No se pudo obtener el resultado de la detección. Vuelva a intentarlo.',
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
