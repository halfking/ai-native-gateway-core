# Explication des données de la page Liste des utilisateurs

Cette page gère les comptes des personnes qui peuvent se connecter et utiliser la plateforme.

## Champs de chaque ligne

Nom du compte, rôle, statut (activé/désactivé), tenant associé, date de dernière activité.

## Rôles et visibilité

- Le rôle détermine les pages visibles après la connexion : les administrateurs voient tout, les administrateurs de tenant uniquement leur propre tenant.
- Un compte « désactivé » ne peut pas se connecter, mais son historique d’utilisation et ses enregistrements de requêtes sont conservés et restent consultables.

## Définition de « dernière activité »

- Retenir l’horodatage le plus récent entre la **dernière connexion et le dernier appel** de cette personne.
- L’affichage de « il y a longtemps » ne signifie pas que le compte présente une anomalie : il est simplement possible qu’il n’ait pas été utilisé pendant cette période.

## Comment vérifier en cas d’écart entre les chiffres

1. Le nombre d’utilisateurs ne correspond pas au nombre de places configurées pour le tenant : une place est une notion d’autorisation, tandis que la liste correspond aux comptes réels ; des comptes supplémentaires peuvent provenir d’un essai ou être des vestiges historiques.
2. Deux comptes portent le même nom : les comptes sont distingués par un ID unique ; les noms identiques n’affectent ni la facturation ni les statistiques ; renommer un compte n’en fusionne pas les données historiques.
3. Un compte vient d’être désactivé mais reste affiché comme actif : le statut prend effet immédiatement, mais « dernière activité » reste bloquée au moment précédant la désactivation. C’est normal.
