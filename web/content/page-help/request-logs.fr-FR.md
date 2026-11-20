# Explications des données de la page des requêtes

Cette page constitue le « journal détaillé » de tous les appels de modèles : chaque requête occupe une ligne et peut être consultée une par une après application des filtres.

## Ce que représente chaque ligne

Une ligne = un appel de modèle. Elle contient : l’heure de l’appel, la clé et le modèle utilisés, le statut (succès/échec), la durée de traitement, le volume de texte en entrée/sortie et le coût de l’appel.

## Règles relatives à la période

- Plages rapides facultatives (de la dernière heure au mois en cours) ou dates de début et de fin personnalisées.
- **Les tenants non par défaut ne peuvent consulter que les 3 derniers jours** : si une période plus large est sélectionnée, le système la ramène automatiquement à 3 jours, ce qui se traduit par un recul du sélecteur de dates.
- Les tenants par défaut peuvent utiliser toutes les plages, notamment les 7 derniers jours, la semaine en cours, le mois en cours et l’année en cours.

## Les filtres courants sont combinés par « ET »

Lorsque plusieurs filtres sont définis (période + clé + modèle + statut + fournisseur), les résultats doivent satisfaire toutes les conditions simultanément. Plus le nombre de conditions est élevé, plus le nombre de résultats est faible.

## Définition des statuts

- « Succès » = le modèle a normalement renvoyé une réponse complète.
- Les statuts d’échec distinguent la cause de l’échec (erreur en amont, contenu bloqué, délai dépassé, etc.) ; chaque requête est classée dans une seule catégorie.

## Comment vérifier les écarts entre les chiffres

1. Une divergence de période est la cause la plus fréquente : la période sélectionnée doit être identique sur cette page et sur la page des statistiques.
2. Les filtres affichent un « sous-ensemble » : supprimez tous les filtres avant de comparer avec le total.
3. L’identité du tenant détermine les données visibles : un tenant non par défaut ne peut consulter que les requêtes de son propre tenant, sur une période de 3 jours maximum.
4. Cette page présente les détails (vérifiables requête par requête), tandis que la page des statistiques présente un résumé (agrégé et dédupliqué) : utilisez cette page pour les contrôles ponctuels et la page des statistiques pour comparer les totaux.
