// freeDiscovery.ts — Free resource discovery page (FreeDiscoveryView) copy.
// Technical terms kept untranslated: api_type, openai-completions, tos_verdict values, Orbi JSON.
export default {
  page: {
      title: 'Free Resource Discovery',
      desc: 'Configure provider templates → scan upstream model lists → review → bulk-import into the free resource pool (wired into OmniFree quota tracking).'
    },
  common: {
      refresh: 'Refresh',
      loading: 'Working…',
      empty: 'No data yet',
      actions: 'Actions',
      enabled: 'Enabled',
      disabled: 'Disabled',
      hideForm: 'Collapse'
    },
  tabs: {
      templates: 'Templates',
      tasks: 'Tasks & Review',
      history: 'Import History'
    },
  presets: {
      title: 'Built-in provider presets',
      create: 'Create',
      exists: 'Created',
      createDone: 'Template created from preset: {name}',
      scannerPending: 'Scanner for this protocol is still in progress; scanning will fail for now',
      keyless: 'keyless (no API key)'
    },
  form: {
      show: '+ Custom template',
      providerCode: 'Provider Code *',
      displayName: 'Display name',
      baseUrl: 'Base URL *',
      apiType: 'API protocol',
      apiKeyEnv: 'API key env var',
      apiKeyEnvHint: 'The key itself is never stored: use a $VAR env reference (e.g. $GROQ_API_KEY); leave empty for keyless.',
      tosVerdict: 'ToS verdict',
      submit: 'Create template',
      created: 'Template created'
    },
  orbi: {
      show: 'Import Orbi template JSON',
      hint: 'Paste the content of an Orbi pi-providers template file (JSON with a top-level providers key). One file may contain multiple providers; a single failure does not block the rest.',
      import: 'Import',
      invalidJson: 'Failed to parse JSON, please check the format',
      done: 'Import finished: {created} created, {failed} failed'
    },
  tpl: {
      listTitle: 'Templates ({n})',
      name: 'Template',
      baseUrl: 'Base URL',
      apiType: 'API protocol',
      keyEnv: 'Key env var',
      tos: 'ToS',
      enabled: 'Enabled',
      createdAt: 'Created at',
      scan: 'Scan',
      delete: 'Delete',
      deleteConfirm: 'Delete template "{name}"? Existing discovery tasks and results are unaffected.',
      deleted: 'Template deleted: {name}'
    },
  scan: {
      title: 'Trigger discovery',
      pickTemplate: 'Pick an enabled template…',
      start: 'Start scan',
      running: 'Scanning…',
      done: 'Scan finished: {n} models found, please review the results',
      failed: 'Scan failed'
    },
  task: {
      listTitle: 'Discovery tasks ({n})',
      provider: 'Provider',
      status: 'Status',
      trigger: 'Trigger',
      found: 'Found',
      imported: 'Imported',
      by: 'By',
      time: 'Created at',
      error: 'Error',
      review: 'Review results'
    },
  res: {
      title: 'Results · task {id} · {provider}',
      pending: 'Pending review',
      all: 'All',
      model: 'Model ID',
      displayName: 'Display name',
      freeType: 'Free type',
      monthly: 'Monthly quota (tokens)',
      daily: 'Daily quota (tokens)',
      tos: 'ToS',
      importStatus: 'Import status',
      none: 'No results under this filter',
      policy: 'Conflict policy',
      policySkip: 'skip: keep existing entries',
      policyOverwrite: 'overwrite: overwrite and re-enable',
      policyMerge: 'merge: fill empty fields only',
      importSelected: 'Import selected ({n})',
      importAllPending: 'Import all pending',
      importedToast: 'Import finished: {imported} imported, {skipped} skipped, {conflicted} conflicted, {failed} failed'
    },
  status: {
      taskPending: 'Waiting',
      running: 'Running',
      success: 'Success',
      failed: 'Failed',
      review: 'Pending',
      imported: 'Imported',
      skipped: 'Skipped',
      conflict: 'Conflict'
    },
  trigger: {
      manual: 'Manual',
      scheduled: 'Scheduled',
      webhook: 'Webhook'
    },
  hist: {
      title: 'Import history ({n})',
      desc: 'Tasks that actually imported models (imported count > 0). Click Detail to see where each result of that task ended up.',
      completed: 'Completed at',
      detail: 'Detail',
      none: 'No import records yet. Finish a bulk import on the Tasks & Review tab and it will show up here.',
      detailTitle: 'Import detail · task {id} · {provider}',
      importedAt: 'Imported at'
    },
}
