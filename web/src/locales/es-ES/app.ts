// Auto-translated draft (es-ES) · 2026-07-02 · please review
// app.ts — Textos del shell de App.vue (roles, colapsar lateral, salir, cambio de idioma).
export default {
  brand: 'AI-Native Org Gateway',
  role: {
    super_admin: 'Superadministrador',
    tenant_admin: 'Administrador de inquilino',
  },
  sidebar: {
    expand: 'Expandir barra lateral',
    collapse: 'Contraer barra lateral',
    collapseMenu: 'Contraer menú',
  },
  logout: 'Cerrar sesión',
  lang: {
    switch: 'Cambiar idioma',
    label: 'Idioma',
  },
  // 2026-07-22: pie del shell público (emparejado con LifecycleShell).
  footer: {
    left: '© 2026 AI-Native Gateway',
    right: '¿Necesita ayuda?',
    feedbackLink: 'Enviar comentarios',
  },
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
  nav: {
    mainAria: 'Navegación principal',
    skip: 'Saltar al contenido principal',
  },

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  v1DataFrozen: {
    title: 'Los datos de tráfico han dejado de actualizarse: las cifras de esta página solo reflejan el periodo anterior a la parada de escritura',
    titleUnconfirmed: 'El interruptor de detención de escritura no está configurado explícitamente: no se puede saber si estas cifras están obsoletas',
    titleUnavailable: 'No se puede confirmar el estado de los datos de tráfico: las cifras de esta página pueden estar obsoletas',
    affects: 'Clases de puntos de lectura afectadas',
    gateKey: 'Ajuste de control',
    retry: 'Volver a comprobar',
    failedHint: 'No se pudo leer el estado de los datos. Un estado no confirmado no es un estado correcto: trata las cifras de esta página como posiblemente obsoletas.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'A newer build is deployed. Reload the page to switch.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'This page carries no deploy seq (not injected at build time)',
    reasonNoRemote: 'Server returned no deploy seq (old build or non-repo hosting)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
