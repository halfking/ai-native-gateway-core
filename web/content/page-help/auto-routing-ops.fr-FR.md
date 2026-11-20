# Explication des données de la page d’exploitation du routage AUTO

Cette page constitue l’atelier de travail en boucle du « réglage automatique » : annotation → statistiques → recommandations de profil → approbation des réglages. Le système sélectionne ainsi de plus en plus le modèle adapté, mais **chaque application doit passer les contrôles et obtenir une approbation manuelle**.

## Les quatre onglets forment un processus continu

- **Atelier d’annotation** : les interventionurs évaluent manuellement la qualité des résultats des requêtes historiques ; ces évaluations constituent la base de toutes les statistiques ultérieures.
- **Statistiques d’annotation** : synthèse du volume d’annotations (annotations en attente aujourd’hui, total des annotations, nombre de participants).
- **Profil des tâches** : analyse des performances des modèles par type de tâche et production de recommandations de profil (« ce type de tâche convient mieux à tel modèle »).
- **Approbation des réglages** : les recommandations de réglage des paramètres proposées par le système y attendent une approbation ou un rejet manuel.

## Explication des règles de calcul

- Les « annotations en attente aujourd’hui » sont calculées par jour civil (UTC) ; les annotations restées en attente la veille sont reprises dans la file des tâches du « total des annotations ».
- Les propositions de réglage sont soumises à un seuil de confiance : le système ne soumet à approbation que les recommandations dont il est certain ; les recommandations à faible confiance sont automatiquement suspendues, sans déranger les approbateurs.
- Avant chaque application effective d’un réglage, le système crée un « instantané de l’ancienne valeur » : après approbation, si le résultat est incorrect, il est possible de revenir en un clic à l’état précédant le réglage.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Il est normal que le nombre d’annotations ne corresponde pas au nombre de requêtes : toutes les requêtes ne nécessitent pas d’annotation, le système en sélectionne uniquement un échantillon représentatif.
2. Une proposition rejetée ne réapparaîtra pas : les recommandations d’une même famille tiennent compte des décisions d’approbation historiques.
3. Les indicateurs ne changent pas après l’application d’un réglage : l’application des paramètres est différée (intervalle de rechargement à chaud) et les effets ne se constatent que sur le nouveau trafic.
4. L’onglet ouvert par lien profond est incorrect : les quatre anciennes pages ont été regroupées dans une seule page à quatre onglets, et les anciens liens redirigent automatiquement vers la page correspondante.
