// hyper.ts — Textos del shell Hyper (navegación inferior / hoja de cuenta / estado de lista / foco / escritorio).
// 2026-10-04, añadidos con el runtime Hyper (docs/UI规范/00 §5.4, H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'Navegación principal',
    home: 'Inicio',
    dashboard: 'Panel',
    requests: 'Solicitudes',
    models: 'Modelos',
    more: 'Más',
  },
  account: {
    title: 'Cuenta',
    profile: 'Perfil',
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
    switchTo: 'Cambiar a {mode}',
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
    empty: 'Sin registros',
  },
  focus: {
    enter: 'Vista enfocada',
    exit: 'Salir del enfoque',
  },
  desktopOnly: {
    banner: 'Esta página está diseñada para escritorio. Puede consultarla aquí; la edición pesada corresponde al escritorio.',
  },
}
