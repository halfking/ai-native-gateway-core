# Explication des données du panneau d’exploitation

Cette page est la page d’accueil de l’espace de travail des équipes d’exploitation : elle rassemble sur un seul écran des indicateurs d’exploitation dispersés (utilisation, approbations, tâches d’arrière-plan).

## Contenu de la page

- **Carte de synthèse de l’utilisation** : volume de requêtes, taux de réussite, dépenses et autres chiffres clés (issus de la même source et selon les mêmes règles de calcul que le tableau de bord).
- **Tâches d’arrière-plan** : statut et heure de la dernière exécution des tâches périodiques du système (agrégation, détection, synchronisation, etc.).
- **Résumé des auto-contrôles** : aperçu rapide de l’état de santé des composants critiques.

## Lecture des tâches d’arrière-plan

- Chaque tâche affiche « heure de la dernière exécution + résultat » ; « Échec » ne signifie pas que le service métier est indisponible — la plupart des tâches seront relancées automatiquement au cycle suivant.
- Il faut donc surveiller les tâches qui ne s’exécutent pas depuis longtemps (au-delà de deux fois leur période).

## Règles de calcul

- Par défaut, les chiffres des cartes correspondent à « Aujourd’hui » (journée UTC) ; après un changement de période, ils sont alignés sur ceux du tableau de bord.
- Cette page n’affiche aucun total agrégé couvrant plusieurs unités ; les cartes de dépenses n’affichent qu’un seul périmètre de calcul, clairement indiqué.

## Comment vérifier les écarts de chiffres

1. Les chiffres des cartes présentent un écart sur quelques entrées avec ceux du tableau de bord : la croissance du trafic en temps réel entre deux rafraîchissements est normale ; fermez la fenêtre « Aujourd’hui » ou attendez le rafraîchissement suivant.
2. Toutes les tâches d’arrière-plan affichent « Échec » : vérifiez d’abord si la couche de stockage (base de données) est indisponible — la plupart des tâches en dépendent.
3. Une tâche vient d’être déployée et n’a encore jamais été exécutée : « Non exécuté » s’affiche ; attendez un cycle.
