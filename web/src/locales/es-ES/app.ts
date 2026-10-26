// Auto-translated draft (es-ES) · 2026-07-02 · please review
// app.ts — Textos del shell de App.vue (roles, colapsar lateral, salir, cambio de idioma).
export default {
  brand: {
    siteTitle: 'AI Native Gateway Central',
    siteTitleLineTwo: 'Gateway Central',
    alt: 'Logotipo de Kaixuan',
  },
  role: {
    super_admin: 'Superadministrador',
    tenant_admin: 'Administrador de inquilino',
  },
  // 2026-07-22: pie del shell público (emparejado con LifecycleShell).
  // 2026-07-22: menú de usuario (UserMenuDropdown) + diálogo de perfil (UserInfoDialog).
  userMenu: {
    profile: 'Perfil',
    changePassword: 'Cambiar contraseña',
    logout: 'Cerrar sesión',
  },
  userInfo: {
    displayName: 'Nombre mostrado',
    username: 'Nombre de usuario',
    email: 'Correo electrónico',
    role: 'Rol',
    tenant: 'Inquilino',
    close: 'Cerrar',
  },
  // 2026-07-22: skip-link (a11y).

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  dataHorizon: {
    title: 'Los datos de tráfico se están quedando atrás: la pasarela atiende solicitudes pero el flujo de sesiones no se mantiene al día',
    titleUnavailable: 'No se puede confirmar el estado de los datos de tráfico: las cifras de esta página pueden estar obsoletas',
    hintLabel: 'Investigar',
    retry: 'Volver a comprobar',
    failedHint: 'No se pudo leer el estado de los datos. Un estado no confirmado no es un estado correcto: trata las cifras de esta página como posiblemente obsoletas.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'Hay una compilación más reciente desplegada. Recarga la página para cambiar.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'Esta página no lleva secuencia de despliegue (no se inyectó en la compilación)',
    reasonNoRemote: 'El servidor no devolvió la secuencia de despliegue (compilación antigua o alojamiento fuera del repo)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
