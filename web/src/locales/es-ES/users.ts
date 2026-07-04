// Auto-translated draft (es-ES) · 2026-07-02 · please review
// users.ts — Página de gestión de usuarios.
export default {
  title: 'Usuarios',
  create: 'Nuevo usuario',
  readOnlyNotice: '📖 Es administrador de inquilino en modo de solo lectura. La gestión de usuarios es de solo lectura; no puede crear, editar ni eliminar usuarios.',
  filter: {
    byTenant: 'Filtrar por inquilino:',
    allTenants: 'Todos los inquilinos',
  },
  table: {
    username: 'Usuario',
    displayName: 'Nombre mostrado',
    email: 'Correo electrónico',
    tenant: 'Inquilino',
    role: 'Rol',
    lastLogin: 'Último acceso',
  },
  action: {
    resetPassword: 'Restablecer contraseña',
  },
  role: {
    super_admin: 'Superadministrador',
    tenant_admin: 'Administrador de inquilino',
  },
  modal: {
    create: {
      title: 'Nuevo usuario',
      username: 'Usuario *',
      password: 'Contraseña *',
      passwordPlaceholder: 'Mínimo 8 caracteres',
      displayName: 'Nombre mostrado',
      email: 'Correo electrónico',
      tenant: 'Inquilino *',
      role: 'Rol',
    },
    reset: {
      title: 'Restablecer contraseña — {name}',
      newPassword: 'Nueva contraseña',
      passwordPlaceholder: 'Mínimo 8 caracteres',
    },
  },
  error: {
    usernamePasswordRequired: 'Usuario y contraseña son obligatorios',
    createFailed: 'Error al crear',
    resetFailed: 'Error al restablecer',
    passwordMinLength: 'La contraseña debe tener al menos 8 caracteres',
  },
  confirmDelete: '¿Eliminar el usuario {name}?',
}
