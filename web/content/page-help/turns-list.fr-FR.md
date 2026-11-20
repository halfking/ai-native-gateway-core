# Explication des données de la page de liste des tours

Cette page organise les appels au modèle selon la logique « session → tour » : une session est un dialogue continu, et un tour correspond à une question et une réponse.

## Relations entre les niveaux

- Projet / Utilisateur / Client / Clé sont quatre perspectives de regroupement : une même série de sessions peut être consultée sous différents angles.
- En développant une session, vous pouvez voir chacun de ses tours : résumé du contenu de la question, résumé de la réponse, durée et statut.

## Comment un « tour » est compté

- Chaque question envoyée par l’utilisateur suivie d’une réponse du modèle constitue un tour.
- Dans une même session, les tours sont classés par ordre chronologique.
- Un tour peut comprendre plusieurs appels internes au modèle (nouvelles tentatives, appels d’outils). Ils sont rattachés à ce tour et ne sont pas comptés séparément.

## Résumés et statuts

- Le résumé d’une session est généré automatiquement par le système ; « Résumer immédiatement » déclenche manuellement une nouvelle génération. Un état d’avancement s’affiche pendant la génération.
- Lorsque l’enregistrement des sessions est indisponible, les nouvelles sessions peuvent ne pas afficher temporairement le détail de leurs tours. Il s’agit d’un état temporaire au niveau de la collecte, et non d’une perte de données.

## Comment vérifier les écarts entre les totaux

1. Cette page agrège les données par « session », tandis que la page des journaux de requêtes les détaille par « appel unique » : une requête peut ne constituer qu’une partie d’un tour dans la vue par session ; les nombres d’éléments sur les deux pages sont donc naturellement différents.
2. Plus vous sélectionnez de dimensions de filtrage (Projet/Utilisateur/Client/Clé), plus le nombre de sessions est faible : effacez les filtres avant de comparer le total.
3. Le chargement des tours est limité (chaque expansion récupère les 100 tours les plus récents) : pour les sessions très longues, réduisez la plage temporelle ou parcourez les pages suivantes.
