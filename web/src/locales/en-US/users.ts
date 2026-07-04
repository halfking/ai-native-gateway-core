// users.ts — Users management page. Reuses common module for shared terms.
export default {
  title: 'Users',
  create: 'New User',
  readOnlyNotice: '📖 You are a tenant admin in read-only mode. User management is view-only; you cannot create, edit, or delete users.',
  filter: {
    byTenant: 'Filter by tenant:',
    allTenants: 'All tenants',
  },
  table: {
    username: 'Username',
    displayName: 'Display name',
    email: 'Email',
    tenant: 'Tenant',
    role: 'Role',
    lastLogin: 'Last login',
  },
  action: {
    resetPassword: 'Reset password',
  },
  role: {
    super_admin: 'Super Admin',
    tenant_admin: 'Tenant Admin',
  },
  modal: {
    create: {
      title: 'New User',
      username: 'Username *',
      password: 'Password *',
      passwordPlaceholder: 'At least 8 characters',
      displayName: 'Display name',
      email: 'Email',
      tenant: 'Tenant *',
      role: 'Role',
    },
    reset: {
      title: 'Reset password — {name}',
      newPassword: 'New password',
      passwordPlaceholder: 'At least 8 characters',
    },
  },
  error: {
    usernamePasswordRequired: 'Username and password are required',
    createFailed: 'Create failed',
    resetFailed: 'Reset failed',
    passwordMinLength: 'Password must be at least 8 characters',
  },
  confirmDelete: 'Delete user {name}?',
}
