# Explication des données de la page d’analyse des corrélations avec la qualité

Cette page répond à la question « Quels facteurs influencent la qualité des réponses ? » : elle analyse statistiquement les corrélations entre les caractéristiques des requêtes (longueur du prompt, présence d’une image, présence de code, nombre d’appels d’outils) et le score de qualité des réponses.

## Blocs principaux

- **Facteurs prédictifs de la qualité** (table des coefficients de corrélation) : intensité de la corrélation entre chaque caractéristique et la qualité (plus |r| est élevé, plus le pouvoir prédictif est fort).
- **Comparaison par tranches** : répartition des requêtes en tranches selon la valeur de chaque caractéristique (par exemple, cinq tranches de longueur du prompt), puis affichage, pour chaque tranche, de la qualité moyenne, du taux de succès et de la latence.

## Définitions et règles de calcul

- Le score de qualité provient de l’évaluation automatique des réponses par le système (synthèse multidimensionnelle) ; il ne s’agit pas d’une note donnée par les utilisateurs.
- Le coefficient de corrélation (Pearson r) varie de -1 à +1 : une valeur positive indique que plus la caractéristique est élevée, plus la qualité est haute ; une valeur négative indique l’inverse ; une valeur proche de 0 indique l’absence de relation.
- Seules les requêtes disposant d’un score de qualité sont prises en compte dans les tranches ; les requêtes non couvertes par l’évaluation ne sont pas intégrées au dénominateur.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Corrélation forte mais inexpliquable du point de vue métier : il peut exister une cause commune (par exemple, « toutes les requêtes contenant une image proviennent du même fournisseur ») ; consultez le détail des tranches.
2. Nombre total de requêtes dans les tranches inférieur au nombre total de requêtes affiché sur la page : les requêtes non évaluées ne sont pas comptabilisées dans les tranches ; l’écart correspond à la part « non évaluée ».
3. Valeur de r différente pour la même characteristic entre deux observations : les périodes observées sont différentes ; r étant sensible à la fenêtre, fixez celle-ci lors des comparaisons.
