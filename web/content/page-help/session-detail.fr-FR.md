# Explication des données de la page de détail de la session

Cette page présente une vue d’ensemble **d’une conversation complète** : ce dont elle parle, le nombre de tours, les questions et les réponses de chaque tour, afin d’aider les gestionnaires à comprendre entièrement le déroulement d’une session de service.

## Ce que vous pouvez voir sur cette page

- **Barre de résumé de la conversation** : résumé du sujet de la conversation, participants, nombre de tours et indication de la session est toujours en cours.
- **Chronologie des tours** : questions et réponses de chaque tour, dans l’ordre, avec possibilité de développer chaque tour pour afficher son contenu intégral.
- **Points clés des tours** : résumé condensé des tours importants, pour repérer rapidement les étapes clés.

## Règles de calcul

- **Une session** désigne une même conversation continue ; tous les chiffres de la page portent uniquement sur **cette session** et ne sont pas cumulés entre plusieurs sessions.
- **Le nombre de tours** est calculé selon l’alternance des questions et des réponses : chaque question posée par l’utilisateur compte pour un tour, quelle que soit la longueur de la réponse.
- **Le résumé** est généré automatiquement par le système à partir de l’ensemble de la conversation. Si la session est toujours en cours, le résumé peut être en retard sur les derniers tours ; l’« Heure de génération » fait foi.
- **L’heure affichée** correspond à l’heure enregistrée par le serveur ; lors de l’affichage depuis un autre fuseau horaire, l’heure système de référence (changement de jour UTC) s’applique, sans conversion vers le fuseau horaire local.

## Comment vérifier soi-même en cas d’écart entre les chiffres

- **Le nombre de tours ne correspond pas à celui de la page de liste** : des filtres peuvent être actifs sur la page de liste ; effacez d’abord tous les filtres, puis comparez à nouveau.
- **Le résumé ne semble pas correspondre à la conversation la plus récente** : tant que la session est en cours, le résumé suit un cycle de génération ; patientez quelques minutes, puis actualisez la page. Si l’« Heure de génération » précède le dernier tour, cela est normal.
- **Le contenu d’un tour ne s’ouvre pas** : il se peut que la durée de conservation des données soit expirée et que ce tour ait été supprimé. Il reste visible dans la liste, mais son contenu intégral n’est plus conservé, conformément à la politique de rétention.
- **Deux pages affichent chacune une session portant le même titre** : utilisez le numéro de session pour vérifier s’il s’agit de la même session ; des titres similaires ne signifient pas qu’il s’agit de la même conversation.
