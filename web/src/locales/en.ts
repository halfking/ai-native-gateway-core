export default {
  // Common
  login: 'Login',
  logout: 'Logout',
  changePassword: 'Change Password',
  cancel: 'Cancel',
  confirm: 'Confirm',
  save: 'Save',
  delete: 'Delete',
  edit: 'Edit',
  search: 'Search',
  reset: 'Reset',
  submit: 'Submit',
  back: 'Back',
  next: 'Next',
  previous: 'Previous',
  close: 'Close',
  
  // User roles
  role: {
    super_admin: 'Super Admin',
    tenant_admin: 'Tenant Admin',
  },
  
  // Navigation
  nav: {
    collapseSidebar: 'Collapse Menu',
    expandSidebar: 'Expand Sidebar',
  },
  
  // Password
  password: {
    changeSuccess: 'Password changed successfully',
    changeFailed: 'Failed to change password',
  },
  
  // Version info
  version: 'Version',
  build: 'Build',

  // 2026-07-02 (request-logs 附件展示): Attachment-related strings, aligned
  // with the reference doc §6 i18n key list.
  requests: {
    list: {
      table: {
        attachmentsTitle: 'Attachments',
        noAttachments: 'No attachments',
      },
    },
    detail_extra: {
      attachmentsTab: 'Attachments',
      attachmentsLoading: 'Loading attachments…',
      noAttachments: 'No attachments',
      clickToPreviewTitle: 'Click to preview',
      download: 'Download',
      downloadOriginal: 'Download original',
      closePreview: 'Close',
    },
  },

  // 2026-07-03 (real-time request stream): Dashboard swim lane.
  dashboard: {
    liveStream: {
      title: 'Real-time request stream',
      connected: 'Connected',
      connecting: 'Connecting…',
      reconnecting: 'Reconnecting…',
      disconnected: 'Disconnected',
      unsupported: 'Not supported',
      pause: 'Pause',
      resume: 'Resume',
      filterAll: 'All statuses',
      filterSuccess: 'Success only',
      filterInProgress: 'In progress',
      filterGroupFailures: 'Failure breakdown',
      filterFailure5xx: 'Server / upstream (5xx)',
      filterFailure4xx: 'Client / auth (4xx)',
      filterFailureTimeout: 'Timeout / network',
      filterFailureNotFound: 'Routing / model not found',
      filterFailureOther: 'Other failures',
      empty: 'Waiting for requests…',
      countTooltip: '{buffer} in buffer / {visible} visible on screen',
      countAria: '{buffer} requests in buffer, {visible} visible',
      legend: {
        model: 'Model family',
        status: 'Status',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: 'Domestic',
        oss: 'Open source',
        other: 'Other',
        success: 'Success',
        inProgress: 'In progress',
        failure: 'Failure',
      },
    },
  },
}