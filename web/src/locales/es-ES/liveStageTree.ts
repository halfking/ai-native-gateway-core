// liveStageTree.ts — Árbol de etapas de la petición (live2) (2026-11-16)
export default {
  title: 'Árbol de etapas de la petición',
  empty: 'No hay peticiones en la ventana con los filtros actuales',
  stage: {
    auth: 'Autenticación / límite',
    sanitize: 'Anonimización',
    approval: 'Aprobación',
    injection: 'Detección de inyección',
    route: 'Enrutamiento',
    compress: 'Compresión',
    firstByte: 'Primer byte',
    restore: 'Restauración de anonimización',
    provider: 'Proveedor / conjunto de tokens',
    meter: 'Medición',
    reply: 'Respuesta',
  },
  state: {
    pending: 'No alcanzado',
    active: 'En curso',
    done: 'Completado',
    skipped: 'No aplica',
    failed: 'Fallido',
    warned: 'Con aviso',
    unmeasured: 'Sin medir',
  },
  value: {
    sanitize: '{n} punto(s)',
    route: '{n} candidato(s)',
    meter: '{n} token',
    injection: 'puntuación {n} ({d})',
    compress: {
      yes: 'Comprimido',
      no: 'Sin comprimir',
    },
  },
  decision: {
    pass: 'Permitido',
    need_approval: 'A la espera de aprobación',
    block: 'Rechazado',
    safe: 'Seguro',
    warn: 'Coincidencia (solo aviso)',
    unknown: 'Sin determinar',
  },
  return: {
    lane: 'La respuesta vuelve al cliente por la vía de retorno',
    label: 'Al cliente',
  },
  band: {
    status: {
      success: 'Correcto',
      in_progress: 'En curso',
      failure: 'Fallido',
    },
    toolsRestore: 'tools restaurado {n}',
    toolsRestored: 'restaurado',
    finalModel: 'Modelo final',
    upstreamModel: 'Modelo del upstream',
    finalModelTip: {
      action: 'Origen: el evento de nodo seleccionado por el enrutamiento (nombre estándar tras la resolución).',
      canonical: 'Origen: el nombre de modelo estándar registrado en la petición.',
      upstreamRaw: 'Origen: la cadena de modelo realmente enviada al upstream.',
      frame: 'Origen: el campo de modelo de la petición.',
      none: 'Sin medir: no hay una fuente de modelo determinable.',
    },
    modelEntry: {
      auto: 'AUTO eligió {model}',
      direct: 'Solicitado {model}',
    },
    chain: {
      auto: 'tipo de tarea auto {task} → coincidence {model}',
      alias: '{from} se resolvió a {model}',
      switch: 'Cambio de modelo {from} → {to}',
    },
    cache: {
      hit: 'Acierto de caché {pct}%',
      na: 'Tasa de acierto de caché —',
      anomaly: 'Base de caché incoherente',
      tip: {
        na: 'El upstream no informó de aciertos de caché; esto no es 0%.',
        unknown: 'El protocolo del upstream no está determinado, el denominador se desconoce.',
        anomaly: 'La tasa supera el 100%, lo que indica dos bases mezcladas.',
        parallel: 'Base: las lecturas de caché van junto a la entrada; denominador = entrada + lecturas de caché.',
        subset: 'Base: las lecturas de caché ya están dentro de la entrada; denominador = entrada.',
      },
    },
  },
  credibility: {
    verified: 'Fiable',
    suspected: 'Posiblemente diluido',
    diluted: 'Diluido',
    unknown: 'Sin determinar',
    unmeasured: 'Fiabilidad sin medir',
    fingerprintMissing: 'No proporcionado',
    measuredAtMissing: 'No proporcionado',
    tip: {
      unmeasured: 'No hay mediciones de fiabilidad para esa credencial y ese modelo de upstream.',
      detail:
        'Veredicto {verdict} | identidad {identity} | capacidad {capability} | estabilidad {consistency} | referencia {reference} | huella del sistema {fingerprint} | medido el {measuredAt}',
    },
  },
  stats: {
    open: 'Desplegar estadísticas (página completa)',
    readout: '{total} en total | {rate}% correcto | {inFlight} en curso',
    title: 'Estadísticas en vivo',
    hint:
      'Misma base que la página de topología en vivo: los últimos 5 minutos y la última hora se leen directamente de la base de datos, y el día en curso procede del agregado diario (con hasta 1 hora de retraso).',
    close: 'Cerrar',
  },
  idle: {
    wait: 'Sin peticiones nuevas durante {ms} ms',
  },
}
