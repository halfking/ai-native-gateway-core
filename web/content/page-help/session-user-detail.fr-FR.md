# Explication des données de la page de détail de l’utilisateur de session

Cette page présente le profil d’utilisation complet **d’une personne** : ses sessions, ses projets, ses tâches, les modèles qu’elle utilise le plus souvent et l’évolution de ses dépenses.

## Composition

- **Carte de synthèse** : nombre de sessions, Token, dépenses, taux de réussite, taux de succès du cache, durée moyenne.
- **Zone de tendance** : courbes quotidiennes du nombre de sessions et des dépenses sur la fenêtre sélectionnée pour cet utilisateur.
- **Tableau de détail** : lignes d’utilisation ventilées par projet / tâche / agent / modèle.

## Périmètre de calcul

- Tous les chiffres couvrent uniquement les sessions **lancées par cet utilisateur** ; sa participation à des sessions collaboratives (lorsqu’il apparaît dans les sessions d’autres personnes) n’est pas comptabilisée.
- Les « dépenses » sont présentées selon deux méthodes de calcul ; la carte « Classement » indique la position de cet utilisateur dans le classement général et son évolution par rapport à la période précédente.

## Comment vérifier les écarts entre les chiffres

1. Total de la page de détail ≠ chiffre de la ligne sur la page de liste : alignez d’abord les périodes couvertes par les deux pages (la page de détail possède son propre sélecteur de période en haut).
2. Total des détails par projet / tâche ≠ total de la synthèse : les sessions non rattachées à un projet ou à une tâche sont comptabilisées uniquement dans le total de la synthèse ; lorsque le tableau de détail comporte une ligne « Non classé » en bas, elle explique l’écart.
3. Le sens de variation du classement est inversé : la variation est calculée sur la base de la **position au classement** (hausse = amélioration du rang), et non sur la variation de la valeur numérique.
