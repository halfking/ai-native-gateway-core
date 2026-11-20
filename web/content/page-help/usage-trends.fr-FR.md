# Explication des données de la page Tendances d’utilisation

Cette page répond en un grand graphique à la question « Comment l’utilisation évolue-t-elle dans le temps » : elle permet de ventiler les données selon plusieurs dimensions (modèle/clé/tenant), d’observer les hausses et baisses et de repérer les anomalies.

## Périmètre des graphiques

- La granularité temporelle peut être réglée sur « par heure/par jour » ; pour l’agrégation par jour, la journée est coupée à minuit en temps universel coordonné (UTC).
- Une ligne plate (ligne zéro) peut avoir deux significations, à distinguer : **un vrai zéro** (aucune utilisation constatée) ou **un résultat non calculé** (source de données non prête). Lorsque la source de données n’est pas prête, la page l’indique clairement ; dans ce cas, la ligne plate ne représente pas zéro.
- Lorsque plusieurs modèles sont sélectionnés, les courbes sont **présentées séparément**, et non empilées ; pour obtenir le total, additionnez les valeurs quotidiennes.

## Les filtres sont combinés par un « et » logique

Après avoir sélectionné une période, un modèle et une clé, le filtrage supplémentaire ne conserve que les résultats qui remplissent simultanément toutes les conditions.

## Comment vérifier un écart entre les chiffres

1. Si les chiffres diffèrent de ceux du tableau de bord : la page Tendances d’utilisation récupère ses données dans une vue agrégée ; les requêtes des dernières minutes ne sont pas encore intégrées à l’agrégat. Dans ce cas, référencez-vous aux chiffres en temps réel du tableau de bord.
2. Si une courbe présente un « intervalle manquant » autour de minuit : lors d’une agrégation par jour traversant minuit UTC, la journée en heure de Pékin correspond à l’après-midi de la veille en UTC. Une différence de point de départ de la fenêtre relève donc d’une différence de périmètre de calcul.
3. Si, après ventilation, la somme des sous-ensembles est différente de la courbe totale : les valeurs de dimension de certaines requêtes sont manquantes (par exemple, le service en amont n’a pas renvoyé de nom de modèle). Ces requêtes sont comptabilisées dans le total, mais pas dans la ventilation — la page indique le volume de la part non classée.
