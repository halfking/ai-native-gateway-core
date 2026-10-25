export default {
  refresh: "Actualizar",
  loading: "Cargando…",
  storage: {
    migrateFailedWithReason: 'No se pudo completar la migración: {reason}. Vuelva a intentarlo.',
  },
  /**
   * Gestión de campos grandes (BlobManager).
   * ★ Estado vacío (2026-10-27 · incremento 14): antes se usaba la clave genérica
   *   `common.feedback.noData` («Todavía no hay contenido que mostrar.»).
   *   Esta página **no tiene filtro de tiempo** ⇒ el texto no menciona ningún
   *   rango temporal (no afirmar lo que la página no sabe).
   */
  blob: {
    emptyNoResult: 'Aún no hay registros de campos grandes.',
  },
  storageOverview: {
    summary: {
      databaseTitle: "Base de datos PostgreSQL",
      databaseSubtitle: "Huella total (tablas + índices + TOAST)",
      databaseMetric: "Base de datos",
      tablesMetric: "Tablas",
      indexesMetric: "Índices",
      toastMetric: "TOAST",
      diskTitle: "Disco local",
      diskUsedOf: "Usado / {total}",
      path: "Ruta",
      used: "Usado",
      free: "Libre",
      columnarTitle: "Almacenamiento columnar (citus_columnar)",
      enabled: "Habilitado",
      notInstalled: "No instalado",
      columnarTableCount: "Número de tablas (incluidas particiones)",
      columnarTableCountMeta: "Tablas que usan el método de acceso columnar",
      columnarTotalColumns: "Total de columnas",
      columnarTotalColumnsMeta: "Suma de columnas de todas las tablas columnar",
      columnarSize: "Tamaño columnar",
      columnarSizeMeta: "Tamaño comprimido (incluye metadatos de chunk)",
      columnarDbShare: "Cuota DB",
      columnarDbShareMeta: "Columnar / tamaño total de base de datos",
      columnarNotInstalledFallback: "La extensión citus_columnar no está instalada",
      localLogsTitle: "Directorio local de logs",
      notFound: "No encontrado",
      fileCount: "Archivos",
      size: "Tamaño",
      oldest: "Más antiguo",
      newest: "Más reciente",
      localLogsMissingHint: "No se encontró el directorio de logs; la rotación / copia gzip no escribirá aquí.",
      warningsTitle: "Advertencias"
    },
    tablesTitle: "Top {n} tablas de la base de datos",
    refresh: "Actualizar",
    refreshLoading: "Cargando…",
    columns: {
      tableName: "Tabla",
      rows: "Filas",
      totalSize: "Tamaño total",
      indexes: "Índices",
      toast: "TOAST",
      percent: "Cuota",
      partition: "Partición",
      actions: "Acciones"
    },
    partitionBadge: "Partición",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table}: sin bloqueo, reclama tuplas muertas",
      vacuumFullTitle: "VACUUM FULL {table}: con bloqueo, devuelve espacio al SO",
      reindexTitle: "REINDEX TABLE {table}: con bloqueo, reconstruye los índices"
    },
    modal: {
      vacuumTitle: "🧹 Confirmar VACUUM",
      vacuumFullTitle: "🗜 Confirmar VACUUM FULL",
      reindexTitle: "🔧 Confirmar REINDEX",
      intro: "Se ejecutará {op} sobre {table}.",
      currentSize: "Tamaño actual: ",
      rowCount: "Número de filas: ",
      partition: "Particionada: ",
      partitionYes: "Sí (⚠️ el bloqueo afecta a todas las particiones hijas)",
      partitionNo: "No",
      flow: "Flujo de la operación",
      impact: "Impacto",
      disk: "Efecto sobre el espacio en disco",
      time: "Duración estimada",
      cancel: "Cancelar",
      confirm: "Ejecutar {op}",
      success: "{op} {table} completado: liberado {size} ({pct}%)",
      failed: "{op} falló: {msg}"
    },
    rowResult: {
      running: "{op} en curso…",
      success: "{op}: liberado {size} ({pct}%, {ms}ms)",
      failed: "{op} falló: {msg}"
    },
    op: {
      vacuum: {
        flow: "Recorre la tabla en busca de tuplas muertas y marca su espacio como reutilizable (sin devolverlo aún al sistema operativo). También actualiza las estadísticas para que el planificador elija mejores planes.",
        impact: "No bloquea la tabla; SELECT / INSERT / UPDATE / DELETE siguen funcionando. Consume algo extra de CPU e IO durante la ejecución.",
        disk: "No devuelve espacio al sistema operativo inmediatamente, sino que lo reutiliza dentro de la tabla. Reduce el crecimiento futuro por INSERT, pero no reduce pg_database_size de inmediato.",
        time: "Habitualmente de segundos a minutos. Coste ≈ tamaño de tabla / IO secuencial (≈200 MB/s en un SSD típico)."
      },
      vacuumFull: {
        flow: "Adquiere un bloqueo ACCESS EXCLUSIVE y reescribe toda la tabla (incluyendo su TOAST) en un nuevo archivo en disco. Luego intercambia el archivo nuevo y libera el antiguo.",
        impact: "⚠️ Mientras se mantiene el bloqueo ACCESS EXCLUSIVE, TODAS las lecturas y escrituras sobre la tabla quedan bloqueadas. Ejecutar en horas de poco tráfico.",
        disk: "El uso de disco se duplica temporalmente (archivo antiguo + nuevo). Al finalizar, el archivo antiguo se libera. El bloat máximo recuperable ≈ tabla + TOAST + índices.",
        time: "Coste ≈ (tabla + índices) / IO de escritura secuencial (≈200 MB/s en un SSD típico). Ejemplo: tabla de 10 GB ≈ 1–3 min, 100 GB ≈ 15–30 min."
      },
      reindex: {
        flow: "Adquiere un bloqueo de tabla y reconstruye cada índice soportado (p. ej. B-tree) uno tras otro. Al finalizar, los índices nuevos sustituyen a los antiguos.",
        impact: "⚠️ El bloqueo bloquea DML (INSERT / UPDATE / DELETE) durante el proceso. Recomendado en horas de poco tráfico.",
        disk: "El espacio de índices se duplica temporalmente (índices antiguos + nuevos). Al finalizar, los antiguos se liberan. Elimina el bloat de índices y puede acelerar lecturas posteriores.",
        time: "Coste ≈ tamaño total de índices / IO de escritura secuencial (≈200 MB/s en un SSD típico). Índices de esta tabla ≈ {size}; espere de segundos a minutos."
      }
    }
  },
  hotPartition: {
    hotTableTitle: "Migración de datos de tablas Hot",
    hotTableDesc: "Promueve filas de tablas Hot a particiones mensuales. Después hace falta VACUUM FULL para devolver realmente el espacio al sistema operativo.",
    retention: {
      all: "Migrar todo ahora",
      "1day": "Conservar último 1 día",
      "3day": "Conservar últimos 3 días",
      "7day": "Conservar últimos 7 días",
      "30day": "Conservar últimos 30 días"
    },
    startMigrate: "Iniciar migración",
    migrating: "Migrando…",
    migrationProgress: "Migradas {migrated} filas ({batches} lotes, {duration}s)",
    promoteConfirm: "¿Migrar de {label} los datos con más de {hours}?",
    promoteAll: "todo",
    days: "{n} días",
    partitionTitle: "Gestión de particiones mensuales",
    partitionDesc: "Elimine particiones mensuales antiguas para liberar espacio en disco.",
    partitionWarning: "Esta acción es irreversible: proceda con precaución.",
    tables: {
      title: "Tablas particionadas",
      refresh: "Actualizar",
      columns: {
        name: "Tabla",
        description: "Descripción",
        totalSize: "Tamaño total",
        rows: "Filas",
        partitions: "Particiones",
        archivable: "Archivables",
        actions: "Acciones"
      },
      viewPartitions: "Ver / gestionar particiones",
      noTables: "Sin tablas particionadas"
    },
    partitionList: {
      title: "Particiones de {table}",
      columns: {
        name: "Partición",
        storageType: "Almacenamiento",
        size: "Tamaño",
        rows: "Filas",
        month: "Mes",
        action: "Acciones"
      },
      storage: {
        columnar: "columnar",
        heap: "heap",
        archive: "archivada"
      },
      delete: "Eliminar",
      deleting: "Eliminando…",
      empty: "Esta tabla no tiene particiones"
    },
    deleteModal: {
      title: "Confirmar eliminación de partición",
      warning: "¡Esta acción es irreversible!",
      fields: {
        name: "Partición",
        size: "Tamaño",
        rows: "Filas",
        month: "Mes"
      },
      inputLabel: "Escriba el nombre de la partición para confirmar:",
      cancel: "Cancelar",
      confirm: "Eliminar partición",
      deleting: "Eliminando…",
    },
    hotTableNames: {
      requestLogs: "Registros de solicitudes",
      credentialModelIndex: "Índice de credenciales y modelos",
      usageLedger: "Libro de uso",
      routingDecisionLog: "Registro de decisiones de enrutamiento"
    },
    labels: {
      rows: "Filas",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'No se pudieron cargar las estadísticas de las tablas hot: {reason}. Vuelva a intentarlo.',
    startMigrationFailedWithReason: 'No se pudo iniciar la tarea de migración: {reason}. Vuelva a intentarlo.',
    submitDeleteFailedWithReason: 'No se pudo enviar la tarea de eliminación: {reason}. Vuelva a intentarlo.',
  },
  preview: "Vista previa de la limpieza",
  hotData: "Datos calientes (0-7 días)",
  warmData: "Datos templados (7-30 días)",
  coldData: "Datos fríos (30-90 días)",
  expiredData: "Datos caducados (más de 90 días)",
  missingDate: "Seleccione una fecha de inicio y una de fin",
  previewFailed: "Error en la vista previa",
  needPreview: "Previsualice primero el impacto",
  executeNotImpl: "La función de limpieza se implementará en una fase posterior. Use el script de línea de comandos para ejecutarla."
,
  pages: {
    filesystemMaintenance: 'Mantenimiento del sistema de archivos',
    storageConfig: 'Configuración de almacenamiento',
    logManagement: 'Gestión de registros',
    usageCost: 'Coste de uso',
    usageEnhanced: 'Análisis avanzado de uso',
  },

  usageEnhanced: {
    refresh: 'Actualizar',
    loading: 'Cargando…',
    empty: 'No hay registros de uso en este periodo.',
    cards: {
      totalCost: 'Coste total',
      totalRequests: 'Solicitudes totales',
      periodDelta: 'Δ de coste entre periodos',
      cacheHitRate: 'Tasa de acierto de caché',
    },
    trend: {
      title: 'Tendencia de coste (últimos {days} días)',
    },
    hotKeys: {
      title: 'Claves API principales (por consumo de tokens)',
      colId: 'ID de clave API',
      colPrefix: 'Prefijo de clave',
      colApplication: 'Aplicación',
      colRequests: 'Solicitudes',
      colTokens: 'N.º de tokens',
      colCost: 'Coste total',
    },
    degraded: {
      title: 'Algunas métricas no están disponibles: ',
      hint: 'El esquema de la base de datos está por detrás de esta versión; las cifras son marcadores de «no se puede calcular», no mediciones reales.',
    },
    errors: {
      load: 'No se pudieron cargar los datos de uso avanzados',
    },
    sources: {
      summary: 'Resumen de uso',
      costTrend: 'Tendencia de coste',
      periodCompare: 'Comparación de periodos',
      cacheEconomics: 'Economía de caché',
      hotKeys: 'Claves API activas',
    },
    footer: 'Última actualización: {time}',
  },

  usageCost: {
    refresh: 'Actualizar',
    loading: 'Cargando…',
    compare: {
      title: 'Comparación de costes',
      currentPeriod: 'Periodo actual:',
      previousPeriod: 'Comparar:',
      currentLabel: 'Actual ({period})',
      previousLabel: 'Periodo anterior ({period})',
      change: 'Cambio',
      significant: 'Significativo',
      meta: '{requests} solicitudes: {models} modelos',
    },
    attribution: {
      title: 'Atribución de costes',
      groupBy: 'Agrupar por:',
      model: 'Por modelo',
      provider: 'Por proveedor',
      intent: 'Por intención',
      shareChart: 'Participación de coste',
      detailChart: 'Desglose (entrada vs salida)',
      otherNote: 'Otros {count} elementos: {cost}',
      otherLabel: 'Otros',
      colModel: 'Modelo',
      colProvider: 'Proveedor',
      colIntent: 'Intención',
      colRequests: 'Solicitudes',
      colTotalCost: 'Coste total',
      colInputCost: 'Coste entrada',
      colOutputCost: 'Coste salida',
      colShare: 'Participación',
      colLatency: 'Latencia media',
    },
    charts: {
      inputCost: 'Coste entrada',
      outputCost: 'Coste salida',
    },
    cache: {
      title: 'Economía de caché',
      hitRate: 'Tasa de aciertos',
      cacheSaved: 'Ahorro por caché',
      cacheSavedHint: 'vs sin caché',
      compressionSaved: 'Ahorro por compresión',
      compressionCount: '{n} compresiones',
      savingsRate: 'Tasa de ahorro global',
      totalSaved: 'Ahorro total {cost}',
      actualSpend: 'Gasto real',
      effectiveRatio: '{pct} del coste sin optimizar',
      totalRequests: 'Solicitudes totales',
    },
    errors: {
      costTrend: 'Error al cargar tendencia de costes',
      periodCompare: 'Error al cargar comparación de periodos',
      cacheEconomics: 'Error al cargar economía de caché',
    },
    degraded: {
      title: 'Algunas métricas no están disponibles:',
      hint: 'El esquema de la base de datos está por detrás de esta versión, así que las cifras siguientes son marcadores de «no se pudo calcular», no mediciones (lo cual no es lo mismo que no haber datos en este periodo). Pide a un administrador que ejecute la migración de agregación de datos.',
    },
  },
  confirmDeleteCleanup: '¿Eliminar {rows} filas?\nEspacio liberado estimado: {size}\n\n¡Esta acción es irreversible!',
  confirmArchiveCleanup: '¿Archivar {rows} filas?\nEspacio liberado estimado: {size}\n\n¡Esta acción es irreversible!',
  confirmAttachmentNullify: '¿Ejecutar?\nSe pondrán en NULL {n} elements\nTotal: {size}\n(no se borran los archivos físicos)',
  confirmBlobCleanup: '¿Limpiar?\nAfecta a {rows} filas\nLibera unos {size}\n(solo pone en NULL request_body / outbound_body; los metadatos se conservan)',
  confirmReplay: '¿Reproducir {filename}?',
  confirmReplayArchive: '¿Reproducir y archivar {filename}?',
  confirmFileCleanup: '¿Eliminar archivos de más de {days} días?\nMotivo: {reason}',
  confirmLogCleanup: '¿Eliminar los archivos de registro de más de {days} días? No se puede deshacer.',
  confirmDirMigrate: 'El directorio de almacenamiento de adjuntos va a cambiar. Los archivos existentes se copiarán al nuevo directorio; el antiguo se borrará tras la verificación.\n\n· Las descargas/escrituras no se ven afectadas durante la copia (sigue el directorio antiguo)\n· Cambia automáticamente al nuevo directorio al terminar\n· Puede tardar con muchos archivos; el progreso se muestra abajo\n\n¿Cambiar el directorio y migrar?',
  dirMigrateCancelled: 'Cancelado (directorio sin cambios)',
}