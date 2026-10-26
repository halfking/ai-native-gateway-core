// tenantModels.ts — Textes de la page /tenant/models (vue locataire : modèles standard + tarifs).
export default {
  page: {
    title: 'Modèles standard',
    desc: 'Modèles standard ouverts pour votre locataire. La tarification couvre l\'entrée / la sortie / la lecture du cache / l\'écriture du cache, facturés en crédits par million de tokens.',
    filterPlaceholder: 'Choisir un modèle standard…',
    modelCount: '{n} modèles',
    refresh: 'Actualiser',
    loading: 'Chargement…',
    empty: 'Aucun modèle disponible',
    loadFailed: 'Échec du chargement',
    vendorSectionTitle: 'Modèles disponibles',
  },
  columns: {
    model: 'Modèle standard',
    contextWindow: 'Contexte',
    multimodal: 'Multimodal',
    billingMode: 'Tarification',
    inPrice: 'Entrée / 1M',
    outPrice: 'Sortie / 1M',
    cacheIn: 'Lecture cache / 1M',
    cacheOut: 'Écriture cache / 1M',
  },
  modalities: {
    text: 'Texte',
    vision: 'Vision',
    audio: 'Audio',
    multimodal: 'Multimodal',
    embedding: 'Embedding',
  },
  billing: {
    token: 'Crédits par token',
  },
  multimodal: {
    yes: 'Oui',
    no: 'Texte uniquement',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    count: '{n} / {m} affichés',
  },
}