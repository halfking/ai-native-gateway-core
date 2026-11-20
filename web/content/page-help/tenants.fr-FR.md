# Explication des données de la page de liste des tenants

Un tenant correspond à l’« organisation cliente » de la plateforme : chaque tenant possède ses propres clés, ses utilisateurs, son quota et sa consommation, et ces éléments sont isolés les uns des autres.

## Champs de chaque ligne

Nom du tenant, code du tenant (code court unique dans le système), statut et utilisation du quota.

## Système de quotas

- **Quota d'abonnement** : quota déduit en priorité et fourni par le forfait mensuel, réinitialisé chaque mois.
- **Solde du portefeuille / solde espèces** : source de facturation à l'usage après épuisement du quota d'abonnement.
- Ordre de consommation : le quota d'abonnement d'abord, puis le portefeuille. La page de détails les présente séparément, sans les regrouper sous un seul « solde ».

## Signification de l'isolation des tenants

- Les clés du tenant A ne peuvent pas modifier la configuration du tenant B ; les statistiques, les journaux et les quotas sont tous imputés séparément par tenant.
- Le « tenant par défaut » est l'espace d'exploitation interne de la plateforme, géré au même titre que les tenants clients payants.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Le quota « restant » ne concorde pas : vérifiez d'abord l'horodatage des statistiques — le quota est déduit en temps réel à chaque appel, et les chiffres de la page peuvent être mis en cache pendant une seconde.
2. La consommation indiquée dans les détails d'un tenant ne correspond pas à celle du tableau de bord général : le tableau de bord inclut la consommation interne du tenant par défaut ; pour les données de facturation d'un tenant, les détails de ce tenant font foi.
3. Un tenant nouvellement créé affiche immédiatement des données « d'hier » : c'est impossible — si vous constatez ce cas, le code du tenant a probablement réutilisé un ancien code ; vérifiez d'abord que le code est unique.
