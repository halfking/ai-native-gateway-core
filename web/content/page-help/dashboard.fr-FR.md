# Explication des données de la page d’ensemble

Cette page est le « tableau de bord » de l’ensemble de la passerelle : elle regroupe sur un seul écran l’utilisation, les coûts et l’état de santé sur une période donnée.

## D’où viennent les données

Tous les chiffres affichés proviennent des flux de chaque appel de modèle enregistrés par la passerelle. Le système consigne d’abord chaque requête, puis les regroupe selon la période sélectionnée afin de produire les différentes statistiques.

## Comment la période est calculée

- Les trois périodes rapides « Aujourd’hui », « 7 derniers jours » et « 30 derniers jours » sont délimitées par jour calendaire. Le début de la fenêtre statistique correspond à minuit le jour même (temps universel coordonné UTC).
- Pour une période personnalisée, la page agrège tous les flux des deux jours sélectionnés : du début du premier jour jusqu’à minuit le dernier jour, en incluant la totalité de ce dernier jour.
- La page s’actualise automatiquement toutes les 10 secondes. Si le canal en temps réel est connecté, les requêtes nouvellement terminées sont ajoutées immédiatement, sans attendre l’actualisation.

## Quatre cartes principales

| Carte | Signification | Algorithme |
| --- | --- | --- |
| Total des requêtes | Nombre d’appels de modèle traités pendant cette période | Comptage direct du nombre de transactions |
| Taux de réussite | Proportion de requêtes ayant renvoyé normalement une réponse | Nombre de requêtes au statut « Réussi » ÷ nombre total |
| Total Token | Volume de texte consommé (entrées + sorties) | Somme des volumes d’entrée et de sortie de chaque appel |
| Coût total | Montant facturé en dollars pendant cette période | Somme des frais de chaque appel (en dollars) |
| Total des crédits | Nombre de crédits selon le référentiel de facturation interne de la plateforme | Somme des crédits déduits pour chaque appel |

Remarque : **les coûts (en dollars) et les crédits relèvent de deux référentiels distincts et ne sont pas additionnés**. Les coûts correspondent au coût en dollars payé aux fournisseurs en amont ; les crédits sont l’unité facturée par la plateforme aux locataires. Les deux valeurs ne sont pas comparables et la page les affiche séparément.

## Nombre d’erreurs et causes d’échec

- Le « nombre d’erreurs » est d’abord obtenu en additionnant les catégories du camembert des causes d’échec ; si le camembert n’est pas disponible, il est estimé avec « Total des requêtes × (1 − taux de réussite) ».
- Cliquer sur la carte du nombre d’erreurs permet d’accéder à la répartition des erreurs et de consulter la part de chaque type d’échec.

## Critères des libellés « Actif »

- « Modèles actifs » correspond au nombre de modèles ayant généré **du trafic réel** pendant la période, et non au nombre total de modèles disponibles sur la plateforme.
- « Clés actives » correspond au nombre de clés actuellement **activées** (paramétrage, indépendamment de la période).
- « Nombre de fournisseurs » correspond au nombre de fournisseurs ayant généré **du trafic réel** pendant la période.

## Comment vérifier soi-même les écarts de chiffres

1. Vérifiez d’abord que les périodes sont identiques : si les fenêtres sélectionnées sur les deux pages diffèrent, les chiffres seront nécessairement différents.
2. Vérifiez si les cartes portent la mention « dégradé » : lorsque les données agrégées ne peuvent pas être calculées temporairement, la page l’indique clairement ; dans ce cas, 0 ne signifie pas « aucun trafic ».
3. Lorsque la carte des crédits affiche « non fiable/non calculé », la source de données de facturation des crédits est temporairement indisponible ; le nombre de requêtes et les coûts restent fiables.
4. Le trafic de sonde n’est pas inclus dans les statistiques métier : les requêtes de contrôle d’état du système lui-même sont exclues du total des requêtes et des coûts.
5. L’écart entre l’actualisation en temps réel et les graphiques est de l’ordre de la seconde : les chiffres des cartes, qui intègrent les ajouts en temps réel, peuvent dépasser légèrement de quelques appels ceux des graphiques ; c’est normal.
