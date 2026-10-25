// English locale for ClientConfigDialog
export default {
  title: '{tool} Configuration Generator',
  close: 'Close',

  step1: {
    title: "① Select an available API key",
    refresh: 'Refresh',
    refreshing: 'Refreshing…',
    loading: 'Loading…',
    empty: {
      title: 'No API Keys',
      description: 'There are no API Keys available under the current tenant. Admins can use the button below to apply for a new key.',
      applyButton: 'Apply for New Key',
    },
    selected: 'Selected:',
  },

  step2: {
    title: '② Operating System',
    pathHint: "Configuration location:",
  },

  step3: {
    title: "④ Select Model Scope",
    featured: "Featured Models (routing for the selected key’s tenant)",
    all: 'All Available Models (from gateway)',
    featuredPreview: {
      empty: "No featured models configured for the selected tenant",
      manageButton: 'Go to "Routing Policy" to configure →',
      manage: 'Manage →',
    },
    allModels: {
      selectAll: 'Select All',
      deselectAll: 'Clear',
      selected: 'selected',
      filterHint: '({count} match search)',
      searchPlaceholder: '🔍 Search model name / vendor / family…',
      noMatch: 'No matching models',
    },
  },

  retry: "Retry",
  otherFamily: "Other",
  gateway: {
    title: "③ Gateway API URL",
    hint: "Uses this gateway’s address by default. Generated files and scripts contain the selected API key; store them securely.",
  },
  provider: {
    label: "Provider name",
    hint: "An entry with the same name and URL is updated. If the URL differs, the script stops and asks you to rename it.",
  },
  notes: {
    cherryStudio: "Enter the selected key in the client settings. This file only lists the parameters.",
    rooCode: "Configure OpenAI Compatible in Roo Code settings.",
  },

  footer: {
    generated: 'Generated {count} model configurations',
    generate: 'Generate Config',
    generating: 'Generating…',
    regenerate: 'Regenerate',
  },

  results: {
    tabs: {
      yaml: "YAML snippet",
      file: 'Config File',
      script: 'Config Script',
      manual: 'Manual Steps',
    },
    actions: {
      scriptWarning: "The script contains your key. Download and run it locally; its contents are not displayed here.",
      mergeHint: "The script backs up the old configuration, merges {provider}, and preserves other providers",
      copyContent: 'Copy Content',
      downloadFile: 'Download File',
      downloadScript: 'Download Script',
    },
  },

  applyDialog: {
    title: 'Apply for New API Key',
    applicationCode: 'Application Code (application_code)',
    applicationCodePlaceholder: 'e.g. default-app',
    description: 'Description',
    descriptionPlaceholder: 'Optional: reason / purpose',
    cancel: 'Cancel',
    submit: 'Submit Application',
    submitting: 'Submitting…',
  },

  error: {
    invalidGatewayURL: "Enter a valid HTTP(S) API URL without credentials, query parameters, or a fragment.",
    invalidProviderName: "Use 1–64 letters (A–Z), digits, underscores, or hyphens for the provider name, starting with a letter or digit.",
    featuredLoadFailed: "Failed to load featured models. Please retry.",
    catalogLoadFailed: "Failed to load the model catalog. Please retry.",
    selectModel: "Select at least one model. If featured models are empty, configure them in Routing Policy first.",
    keyUnavailable: "The selected key could not be read. Please select it again.",
    generateFailed: "Configuration generation failed. Please retry.",
    copyFailed: "Copy failed. Please download the file.",
    applyFailed: 'Application failed',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor does not support file writes: please configure manually in the Settings UI',
}
