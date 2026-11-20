# Explication des données de la page d’analyse des conversations

Cette page répond à la question « Comment est-ce utilisé ? » : combien de conversations, leur durée moyenne, les tâches auxquelles elles sont consacrées et leur évolution.

## Indicateurs clés

| Carte | Signification | Mode de calcul |
| --- | --- | --- |
| Nombre total de conversations | Nombre de dialogues sur la période | Décompte dédupliqué : une conversation n’est comptée qu’une fois, quel que soit son nombre de tours |
| Nombre moyen de Token | Quantité moyenne de texte consommée par conversation | Nombre total de Token sur toutes les conversations ÷ nombre total de conversations |
| Durée moyenne d’une conversation | Durée moyenne de chaque conversation | Moyenne des écarts entre l’horodatage initial et final de chaque conversation |
| Nombre moyen de tours | Nombre moyen d’allers-retours question-réponse par conversation | Nombre total de tours ÷ nombre total de conversations |
| Coût interne (USD) | Coût en dollars payé par la plateforme aux fournisseurs en amont | Addition de chaque transaction selon la tarification du fournisseur |
| Coût externe (crédits) | Crédits facturés aux locataires | Addition de chaque transaction selon la tarification de la plateforme |

**Les coûts interne et externe ne sont pas additionnés** : les dollars et les crédits sont deux unités distinctes, et leur somme n’a aucun sens métier. Le graphique de tendance en haut de la page présente également ces deux unités séparément.

## Tendances et classements

- Le graphique de tendance agrège les données par jour (changement de journée en UTC) et affiche l’évolution quotidienne du nombre de conversations ou des coûts.
- « Par rapport à la période précédente » correspond au pourcentage d’évolution entre la fenêtre actuelle et une fenêtre précédente de même longueur.
- La page de classement (personnes/agents/modèles) trie les données selon l’indicateur sélectionné ; la colonne « Dépense » additionne les deux mesures, **elle sert uniquement à comparer des niveaux relatifs et ne doit pas être interprétée comme un montant**.

## Fraîcheur des données

Cette page utilise un « instantané agrégé » périodiquement calculé par le système, et non le détail de chaque transaction. L’instantané est considéré comme frais pendant environ 90 minutes ; au-delà, la page affiche un bandeau « Données agrégées en cours d’actualisation » et refuse d’afficher des chiffres obsolètes — il s’agit d’un dispositif de protection, et non d’une panne.

## Comment vérifier les écarts entre les chiffres

1. Périmètre temporel : la fenêtre de cette page est différente de celle de la page de l’historique des requêtes ; les chiffres sont donc différents. Commencez par aligner les périodes.
2. Latence de l’instantané : les requêtes les plus récentes peuvent ne pas encore figurer dans l’instantané agrégé ; comparez de nouveau après l’actualisation.
3. Différence de périmètre : cette page agrège par « conversation », tandis que la page de l’historique des requêtes détaille chaque « appel » ; les nombres de lignes ne sont donc pas censés être égaux.
4. Règle de déduplication : le nombre de conversations est dédupliqué par conversation ; plusieurs tours dans une même conversation ne sont pas comptés plusieurs fois. Le nombre de tours, lui, est compté tour par tour.
