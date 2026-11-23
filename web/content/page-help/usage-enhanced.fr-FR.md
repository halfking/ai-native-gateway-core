# Explication des données de la page d’analyse renforcée de l’utilisation

Cette page juxtapose les résultats de cinq API : synthèse, tendance des coûts, comparaison mensuelle, économie du cache et principales clés API. Il ne s’agit pas de mesures répétées provenant d’une même table ou d’une même période.

## Cartes principales

- **Coût total (USD)** : montant total des frais versés aux fournisseurs en amont pendant la période.
- **Nombre total de requêtes** : nombre de requêtes sur la même période.
- **Variation par rapport à la période précédente** : pourcentage de comparaison entre la période actuelle et la période précédente de même durée.
- Ventilation détaillée : répartition des contributions par dimensions telles que le modèle, la clé ou le jour.

## Définitions des indicateurs

- La page combine cinq endpoints indépendants. La synthèse et les principales clés API utilisent une fenêtre en nombre de jours ; la tendance des coûts et l’économie du cache utilisent la plage de dates sélectionnée ; la carte de comparaison compare le mois calendaire en cours au mois précédent. Les sources et périodes diffèrent : les valeurs ne sont donc pas nécessairement identiques à celles du tableau de bord ni entre elles.
- L’endpoint de comparaison additionne les coûts en USD payés aux fournisseurs pour chaque mois calendaire et calcule (coût du mois en cours − coût du mois précédent) ÷ coût du mois précédent. Il ne vérifie pas que les mois sont complets. Si le coût du mois précédent est nul, le taux de variation vaut zéro ; cela ne prouve ni que les deux mois sont complets ni que l’utilisation est restée stable.

## Comment vérifier les chiffres en cas d’écart

1. Pour comparer la carte de période aux indicateurs de la plage sélectionnée, gardez à l’esprit que la carte compare toujours le mois en cours au mois précédent et ne suit pas les dates de début et de fin choisies.
2. La tendance des coûts regroupe les enregistrements par dimension. Les dimensions manquantes sont regroupées sous « unknown » et les groupes de longue traîne peuvent être réunis dans « Other ». Les totaux peuvent différer si les sources ou périodes ne correspondent pas exactement.
3. La synthèse et les principales clés API utilisent une fenêtre en nombre de jours, tandis que la tendance des coûts et l’économie du cache utilisent la plage sélectionnée. Les limites de période, l’actualisation et la journée en cours peuvent créer des écarts. En présence d’un avis de données dégradées, zéro ou un résultat vide ne correspond pas à un zéro mesuré.
