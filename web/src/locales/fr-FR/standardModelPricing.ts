// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// standardModelPricing.ts — Page de tarification des modèles standard (StandardModelPricingView).
// Namespaces : page / desc / settings / table / editModal / status / feedback.
// Termes techniques conservés non traduits : canonical_name, credits_per_1m_in/out/cache_in/cache_out, cents_per_credit, global_discount, CNY/USD.
export default {
  page: {
    title: 'Gestion de la tarification',
    refresh: 'Actualiser',
    refreshLoading: 'Chargement…',
  },
  desc: 'Tarification côté locataire (crédits / 1M tokens). La base globale × remise s\'applique aux dimensions <strong>non manuelles</strong> ; les champs manuels ne sont pas affectés par les modifications globales.',
  settings: {
    title: 'Base globale (crédits / 1M tokens)',
    inputToken: 'Tokens d\'entrée',
    outputToken: 'Tokens de sortie',
    cacheReadToken: 'Tokens de lecture cache',
    cacheWriteToken: 'Tokens d\'écriture cache',
    discount: 'Remise globale',
    discountHint: 'Appliquée uniformément à toutes les dimensions non manuelles',
    centsPerCredit: 'Prix unitaire du crédit (centimes/crédit)',
    currencyDisplay: 'Devise d\'affichage',
    save: 'Enregistrer les paramètres globaux',
    saving: 'Enregistrement…',
    effective: 'Effectif ≈ {value} (remise incluse)',
  },
  filter: {
    summary: 'Manuel {custom}: Global {defaults} / {total}',
  },
  table: {
    colModel: 'Modèle standard',
    colStatus: 'Statut',
    edit: 'Tarification',
    reset: 'Réinitialiser',
    empty: 'Aucun modèle',
    statusDefault: 'Global',
    statusFullCustom: 'Entièrement manuel',
    statusPartialCustom: '{n}/4 manuel',
    manualTagTitle: 'Tarification manuelle',
    manualTag: 'M',

    colModality: "Modalité",

    modalityText: 'Text',

    modalityVision: "Images",

    modalityAudio: 'Audio',

    modalityVideo: 'Video',

    modalityMultimodal: 'Multimodal',

    modalityEmbedding: 'Embedding',

    modalityOther: "Autres",

    hasMultimodal: "Avec tarification multimodale manuelle",
  },
  editModal: {
    title: 'Tarification manuelle: {name}',
    hint: 'Cochez « Manuel » et renseignez les crédits ; les dimensions non cochées continuent de suivre la base globale × remise.',
    fieldSuffix: ': tarification manuelle',
    resetBase: 'Réinitialiser à la base',
    globalApprox: 'Global ≈ {value}',
    save: 'Enregistrer',
    saving: 'Enregistrement…',
    fillGlobal: "Appliquer les valeurs globales actuelles",
    cancel: 'Annuler',
    resetConfirm: 'Réinitialiser toutes les dimensions de {name} à la base globale ?',

    sectionText: "Dimensions des jetons de texte",

    sectionMultimodal: 'Dimensions de jetons multimodaux (vision / audio / vidéo)',



  },
  field: {

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },

  market: {
    open: "Prix du marché",
    title: "Comparaison des prix du marché (flux salesdecorate)",
    syncAsOf: "Au {date} · {rows} lignes intégrées",
    resolved: "{n} résolus vers ce gateway",
    searchPlaceholder: "Rechercher un modèle…",
    onlyResolved: "Résolus uniquement",
    search: "Rechercher",
    empty: "Données de marché vides - lancez scripts/market-price-sync.py sur 211 ou attendez la collecte quotidienne",
    colModel: "Modèle",
    colMarketMin: "Minimum marché entrée/sortie (CNY/1M)",
    colMarketMinOut: "Minimum marché sortie (CNY/1M)",
    colVendor: "Ancre constructeur entrée/sortie (CNY/1M)",
    colRetail: "Notre prix entrée/sortie (CNY/1M)",
    colPlatforms: "Plateformes",
    colDrift: "Retail / marché",
    unresolved: "Non résolu",
  },

  history: {
    title: "Historique des prix",
    changesTitle: "Changements de tarifs",
    open: "Historique",
    openChanges: "Changements de tarifs",
    empty: "Aucune période de prix n'a été enregistrée pour ce modèle.",
    current: "actuel",
    until: "jusqu'au {time}",
    mode: "Mode de facturation",
    source: "Source",
    modePayAsYouGo: "À l'usage",
    modeTokenPlan: "Forfait de tokens",
    modeBooster: "Pack supplémentaire",
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: 'Base globale uniquement',
  customOnly: 'Avec tarification manuelle',
  partialOnly: 'Partiellement manuel',
  otherVendor: 'Autre',
  loadFailed: 'Le chargement de la tarification des modèles standards a échoué. Veuillez réessayer.',
  saved: 'Base globale enregistrée (n\'affecte que les dimensions non manuelles)',
  saveFailed: 'L\'enregistrement de la tarification des modèles standards a échoué. Veuillez réessayer.',
  needOneManual: 'Veuillez cocher au moins une dimension « tarification manuelle »',
  resetFailed: 'La réinitialisation de la tarification des modèles standards a échoué. Veuillez réessayer.',
  input: 'Entrée',
  output: 'Sortie',
  cacheRead: 'Lecture cache',
  cacheWrite: 'Écriture cache',

  batch: {
    selected: '{n} sélectionnés',
    paste: 'Coller sur la sélection',
    price: 'Tarification par lot',
    resetAll: 'Tout réinitialiser sur la base globale',
    resetAllConfirm: 'Réinitialiser toute la tarification manuelle de {n} modèles à la base globale ?',
    fillGlobalConfirm: 'Écrire la base globale × remise actuelle dans {n} modèles (en conservant les indicateurs manuels) ?',
    msgPasted: 'Modèles mis à jour : {n}',
    msgReset: '{n} modèles réinitialisés',
    msgFilled: '{n} modèles renseignés',
    msgFailed: 'L\'opération par lot n\'a pas pu aboutir. Veuillez réessayer.',

    fillGlobal: "Appliquer les valeurs globales actuelles",
  },
}
