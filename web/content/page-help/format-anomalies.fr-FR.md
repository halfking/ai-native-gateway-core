# Explication des données de la page des anomalies de format

Cette page surveille deux types d'anomalies « qui clochent » : un changement soudain de **format** dans les réponses du service en amont, ou une **erreur** de requête. L'objectif est de repérer les changements côté fournisseur avant que les utilisateurs ne les remarquent.

## Deux sources de données (deux onglets)

- **Anomalie de format de réponse** : la structure de la réponse du service en amont ne correspond pas à ce qui est attendu (champ manquant, type modifié, échec de l'extraction du Token, etc.).
- **Erreur de requête** : la requête elle-même a échoué (délai dépassé, refus, paramètre incorrect, etc.).

Le nombre affiché sur le badge en haut de la page = **la somme** des entrées « non résolues » des deux sources de données ; il est calculé de la même manière que le filtre « non résolues » de la page de liste.

## Comment sont calculés les statuts « non résolu / résolu »

- Toute anomalie nouvellement détectée est par défaut « non résolue » ; elle passe à « résolue » après confirmation manuelle du traitement.
- « Résolu » ne signifie pas que l'anomalie a disparu d'elle-même : c'est un statut validé manuellement, qui permet de distinguer « vu » et « pas vu ».

## Comment vérifier soi-même en cas d'écart entre les chiffres

1. Le nombre du badge ne correspond pas à celui de la liste : vérifiez d'abord les filtres (« uniquement non résolues » est-il coché, plage de temps).
2. Une refonte du fournisseur peut faire apparaître tout un lot d'anomalies de même type : regroupez les anomalies par « type d'anomalie » ; un lot du même type correspond généralement à une seule cause.
3. Le trafic de sondage comme le trafic réel peuvent déclencher des enregistrements : avant d'évaluer l'impact, distinguez de quel type de trafic provient l'anomalie.
4. La résolution automatique d'une anomalie (le fournisseur revient à sa version précédente) ne la marque pas automatiquement comme « résolue » : une confirmation manuelle reste nécessaire. C'est un choix délibéré, pour éviter que des changements silencieux passent inaperçus.
