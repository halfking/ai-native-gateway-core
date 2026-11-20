# Explication des données de la page d’analyse renforcée de l’utilisation

Cette page rassemble en une vue synthétique les données d’utilisation dispersées dans cinq API : coût total, nombre total de requêtes, variation par rapport à la période précédente et ventilation détaillée, le tout sur un seul écran.

## Cartes principales

- **Coût total (USD)** : montant total des frais versés aux fournisseurs en amont pendant la période.
- **Nombre total de requêtes** : nombre de requêtes sur la même période.
- **Variation par rapport à la période précédente** : pourcentage de comparaison entre la période actuelle et la période précédente de même durée.
- Ventilation détaillée : répartition des contributions par dimensions telles que le modèle, la clé ou le jour.

## Définitions des indicateurs

- Les données de cette page proviennent de l’API d’agrégation qui alimente également le tableau de bord ; les chiffres doivent correspondre à ceux du tableau de bord. En cas d’écart, aligner d’abord les périodes.
- L’affichage de la « variation par rapport à la période précédente » nécessite des données complètes pour les deux périodes ; lorsqu’il manque un jour, la page affiche « Données incomplètes ». Dans ce cas, le pourcentage est fourni à titre indicatif.

## Comment vérifier les chiffres en cas d’écart

1. Le pourcentage de variation par rapport à la période précédente semble contre-intuitif (l’utilisation n’a pas changé, mais une forte variation s’affiche) : une période précédente incomplète fausse la base de comparaison ; vérifier d’abord l’exhaustivité des données de cette période.
2. Le total de la ventilation détaillée ≠ celui de la carte principale : les requêtes dont la valeur de dimension est manquante sont comptabilisées uniquement dans le total (la page indique le volume non classé).
3. La période inclut aujourd’hui : les chiffres du jour continuent de croître en temps réel et la variation par rapport à la période précédente évolue en continu ; le résultat est plus fiable après la clôture de la journée.
