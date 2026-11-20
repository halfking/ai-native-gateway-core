# Explication des données de la page listant les utilisateurs de session

Cette page présente la consommation par « personne » : chaque utilisateur occupe une ligne, qui récapitule son nombre de sessions, ses Token, ses dépenses et les modèles les plus utilisés.

## Champs de chaque ligne

Utilisateur (nom affiché/compte), nombre de sessions, nombre de Token, dépenses, taux de réussite, liste Top des modèles les plus utilisés (à consulter en développant la ligne).

## Relation avec la vue d’ensemble de l’analyse des sessions

- La page de synthèse agrège les données de tous les utilisateurs ; cette page ventile les données de la même source **par personne**. Lorsque la période couverte est identique, les chiffres des deux pages concordent.
- La page de classement est une vue triée de cette page (Top N) ; cette page contient la liste complète et permet d’effectuer une recherche.

## Règles de calcul

- La colonne « Dépenses » affiche deux méthodes de calcul en parallèle, sans conversion de l’une à l’autre.
- Les sessions sont attribuées à l’« initiateur » : l’utilisation des plusieurs participants à une même session est imputée au compte de l’initiateur.

## Comment vérifier les écarts entre les chiffres

1. Le nombre d’utilisateurs dépasse celui du système : cette page recense les profils ayant eu une activité de session (y compris les comptes système/service) ; pour obtenir la liste nominative des personnes, consultez la page de liste des utilisateurs.
2. Le total de l’utilisation des modèles sur la ligne dépliée ≠ le nombre total de Token sur la ligne : la liste dépliée ne présente que les modèles Top ; les modèles moins utilisés sont regroupés sous « Autres ».
3. Une même personne apparaît sur deux lignes : une modification du compte ou du nom affiché crée deux identités historiques. L’activité est attribuée par compte ; lors des vérifications, utilisez la colonne « Compte ».
