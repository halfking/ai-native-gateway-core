// English locale for ClientConfigDialog
export default {
  title: '{tool} Configuration Generator',
  close: 'Close',
  
  step1: {
    title: '① Select API Key (all keys under current tenant)',
    refresh: 'Refresh',
    refreshing: 'Refreshing…',
    loading: 'Loading…',
    empty: {
      title: 'No API Keys',
      description: 'There are no API Keys available under the current tenant. Admins can click the button below to apply for a new key.',
      applyButton: 'Apply for New Key',
    },
    selected: 'Selected:',
  },
  
  step2: {
    title: '② Operating System',
    pathHint: 'Config file path:',
  },
  
  step3: {
    title: '③ Select Model Scope',
    featured: 'Featured Models (routing featured config)',
    all: 'All Available Models (from gateway)',
    featuredPreview: {
      loading: 'Loading…',
      empty: 'No featured models configured in routing',
      manageButton: 'Go to "Routing Policy" to configure →',
      manage: 'Manage →',
    },
    allModels: {
      selectAll: 'Select All',
      deselectAll: 'Clear',
      selected: 'selected',
      filterHint: '({count} match search)',
      searchPlaceholder: '🔍 Search model name / vendor / family…',
      loading: 'Loading…',
      noMatch: 'No matching models',
    },
  },
  
  footer: {
    generated: 'Generated {count} model configurations',
    generate: 'Generate Config',
    generating: 'Generating…',
    regenerate: 'Regenerate',
  },
  
  results: {
    tabs: {
      file: 'Config File',
      script: 'Config Script',
      manual: 'Manual Steps',
    },
    actions: {
      copyContent: 'Copy Content',
      downloadFile: 'Download File',
      downloadScript: 'Download Script',
      scriptHint: 'Script automatically backs up old config files',
    },
  },
  
  applyDialog: {
    title: 'Apply for New API Key',
    close: 'Close',
    applicationCode: 'Application Code (application_code)',
    applicationCodePlaceholder: 'e.g. default-app',
    description: 'Description',
    descriptionPlaceholder: 'Optional: reason / purpose',
    cancel: 'Cancel',
    submit: 'Submit Application',
    submitting: 'Submitting…',
  },
  
  error: {
    applyFailed: 'Application failed',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor does not support file writes — please configure manually in the Settings UI',
}
