// hyper.ts — Textos del shell Hyper (navegación inferior / hoja de cuenta / estado de lista / foco / escritorio).
// 2026-10-04, añadidos con el runtime Hyper (docs/UI规范/00 §5.4, H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'Navegación principal',
    more: 'Más',
  },
  account: {
    title: 'Cuenta',
    language: 'Idioma',
    theme: 'Tema',
    help: 'Ayuda',
    health: 'Estado del servicio',
    healthy: 'Accesible',
    unhealthy: 'No accesible',
    unknown: 'Estado desconocido',
    logout: 'Cerrar sesión',
    adminEntry: 'Consola de administración',
    close: 'Cerrar',
  },
  dataView: {
    table: 'Tabla',
    cards: 'Tarjetas',
  },
  list: {
    refreshing: 'Actualizando…',
    loadMore: 'Cargar más',
    loadingMore: 'Cargando…',
    retry: 'Reintentar',
    allLoaded: '{count} cargados en total',
    loadFailed: 'Error al cargar. Toque para reintentar.',
  },
}
