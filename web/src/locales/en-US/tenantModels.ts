// tenantModels.ts — /tenant/models page (tenant view: standard models + pricing).
// Namespace: tenants.models.* / tenants.modelsModalities.* / tenants.modelsBilling.*
export default {
  page: {
    title: 'Standard models',
    desc: 'Standard models we have opened for your tenant. Pricing covers input / output / cache read / cache write, all charged in credits per million tokens.',
    filterTitle: 'Standard models · filter',
    filterPlaceholder: 'Pick a standard model…',
    modelCount: '{n} models',
    refresh: 'Refresh',
    loading: 'Loading…',
    empty: 'No models available',
    loadFailed: 'Failed to load',
    vendorSectionTitle: 'Available models',
  },
  columns: {
    model: 'Standard model',
    canonicalName: 'Model ID',
    family: 'Family',
    contextWindow: 'Context',
    multimodal: 'Multimodal',
    modalityTag: 'Type',
    billingMode: 'Billing',
    inPrice: 'Input / 1M',
    outPrice: 'Output / 1M',
    cacheIn: 'Cache read / 1M',
    cacheOut: 'Cache write / 1M',
  },
  modalities: {
    text: 'Text',
    vision: 'Vision',
    audio: 'Audio',
    multimodal: 'Multimodal',
    embedding: 'Embedding',
  },
  billing: {
    token: 'Per-token credits',
  },
  multimodal: {
    yes: 'Yes',
    no: 'Text only',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: 'Selected vendor: {vendor}',
    count: 'Showing {n} / {m}',
  },
  cta: {
    buyCredits: 'Buy credits',
  },
}