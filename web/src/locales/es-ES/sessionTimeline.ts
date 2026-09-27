// sessionTimeline.ts — SessionTurnsTimeline.vue textos (OBS-FE5 línea de tiempo de turnos)
// Cubre: latencia nula / actualizar / error / vacío / cargar más / todos cargados / error de red
export default {
  latencyUnknown: 'Desconocido',
  refresh: 'Actualizar',
  refreshing: 'Actualizando…',
  retry: 'Reintentar',
  empty: 'Sin turnos en esta sesión',
  loading: 'Cargando…',
  loadMore: 'Cargar más turnos',
  bodyUnavailable: 'No se capturó el cuerpo de la solicitud/respuesta en {n} de los turnos cargados; el almacenamiento de cuerpos puede estar desactivado para esta sesión.',
  allLoaded: '{n} turnos cargados, todos completos',
  errors: {
    network: 'Error de red, verifique la conexión y reintente',
  },
}
