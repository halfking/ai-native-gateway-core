# Explication des données de la page de liste des fournisseurs

Cette page gère les fournisseurs en amont intégrés (« OpenAI », « Claude », etc. — services de modèles), ainsi que les identifiants et les modèles associés à chaque fournisseur.

## Différence entre les deux statuts

- **Santé du système** : disponibilité au niveau des identifiants (normal/avertissement/inaccessible/inconnu), d’après les échecs constatés lors des contrôles et du trafic réel.
- **Routabilité** : indique si le fournisseur **participe actuellement à la sélection des routes** (disponible/indisponible/sans modèle/désactivé manuellement). Un fournisseur peut être en bonne santé tout en étant désactivé manuellement — dans ce cas, il ne reçoit aucun trafic, mais les données de santé continuent d’être mises à jour.

## Relation entre les modèles et les identifiants

- Un fournisseur peut être associé à plusieurs identifiants et prendre en charge plusieurs modèles ; un problème avec un seul identifiant ne concerne que les modèles qu’il dessert.
- « Sans modèle » = le fournisseur est connecté, mais aucun modèle disponible n’a encore été configuré ; il ne recevra donc aucun trafic.

## Tri et filtrage

- Le tri par défaut repose sur l’état de santé. Le filtre de disponibilité (routable/non routable) est indépendant : les deux filtres ne s’influencent pas et déterminent uniquement les lignes affichées.

## Comment vérifier les écarts lorsque les chiffres ne correspondent pas

1. Fournisseur en bonne santé mais sans trafic : vérifiez d’abord la « routabilité » — les fournisseurs désactivés manuellement ne participent pas à la sélection des routes.
2. Nombre de modèles différent du nombre de modèles réellement sélectionnables : la liste des modèles est filtrée selon la disponibilité des identifiants ; lorsque tous les identifiants sont hors ligne, les modèles disparaissent temporairement des options.
3. Identifiant récemment enregistré affiché comme « en cours de contrôle » : après l’enregistrement, le système lance automatiquement un contrôle de santé et affiche un état transitoire jusqu’à l’arrivée des résultats.
4. Filtres conservés après une actualisation : la page mémorise les critères de filtrage précédents ; lorsqu’une autre personne utilise la page, effacez d’abord les filtres.
