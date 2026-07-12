// tenantModels.ts — Textos de la página /tenant/models (vista del inquilino: modelos estándar + precios).
export default {
  page: {
    title: 'Modelos estándar',
    desc: 'Modelos estándar habilitados para su inquilino. La facturación cubre entrada / salida / lectura de caché / escritura de caché, en créditos por millón de tokens.',
    filterTitle: 'Modelos estándar · filtro',
    filterPlaceholder: 'Elegir un modelo estándar…',
    modelCount: '{n} modelos',
    refresh: 'Actualizar',
    loading: 'Cargando…',
    empty: 'No hay modelos disponibles',
    loadFailed: 'Error al cargar',
    vendorSectionTitle: 'Modelos disponibles',
  },
  columns: {
    model: 'Modelo estándar',
    canonicalName: 'ID del modelo',
    family: 'Familia',
    contextWindow: 'Contexto',
    multimodal: 'Multimodal',
    modalityTag: 'Tipo',
    billingMode: 'Facturación',
    inPrice: 'Entrada / 1M',
    outPrice: 'Salida / 1M',
    cacheIn: 'Lectura caché / 1M',
    cacheOut: 'Escritura caché / 1M',
  },
  modalities: {
    text: 'Texto',
    vision: 'Visión',
    audio: 'Audio',
    multimodal: 'Multimodal',
    embedding: 'Embedding',
  },
  billing: {
    token: 'Créditos por token',
  },
  multimodal: {
    yes: 'Sí',
    no: 'Solo texto',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: 'Proveedor seleccionado: {vendor}',
    count: 'Mostrando {n} / {m}',
  },
  cta: {
    buyCredits: 'Comprar créditos',
  },
}