# Explication des données de la page de gestion des proxys

Cette page permet de gérer l’« sortie proxy » utilisée par la passerelle pour accéder aux services en amont : source de l’abonnement, pool de nœuds, sortie actuellement utilisée.

## Trois onglets

- **Vue d’ensemble de l’état** : cartes récapitulatives (nombre d’abonnements, nombre total de nœuds, nombre de nœuds joignables/sains) + sortie actuellement sélectionnée + état du basculement automatique.
- **Gestion des abonnements** : sources d’abonnement proxy (liens permettant d’importer un lot de nœuds), avec possibilité de définir une « liste noire de régions » pour filtrer les sorties.
- **Gestion des nœuds** : enregistrement et maintenance manuels, nœud par nœud.

## Repères clés

- **Joignable**＝un nœud actuellement capable d’établir une connexion ; **sain**＝un nœud dont les sondes consécutives sont normales. Un nœud joignable mais échouant de manière répétée n’est pas considéré comme sain.
- **Basculement automatique** : lorsque la sortie actuelle atteint le seuil d’échecs consécutifs, le système passe automatiquement au nœud suivant ; le « nombre d’échecs consécutifs » est cumulé en temps réel et remis à zéro après une réussite.
- La « dernière sonde active » correspond à l’heure du dernier contrôle de connectivité lancé par le système, et non à celle d’une requête métier.

## Comment vérifier en cas d’écart entre les chiffres

1. Nombre total de nœuds ≠ nombre de nœuds joignables : c’est normal — le total correspond au nombre de nœuds enregistrés, tandis que le nombre de nœuds joignables correspond au nombre de nœuds actuellement connectés.
2. Le service métier échoue toujours après un basculement : le basculement suit un cycle de détection et s’effectue en quelques secondes ; en cas d’échec persistant, vérifier si le « nombre d’échecs consécutifs » continue d’augmenter.
3. Le nombre de nœuds ne change pas après la mise à jour d’un abonnement : le contenu de la source d’abonnement est peut-être identique ; comparer les empreintes des nœuds avant et après la mise à jour plutôt que leur nombre.
4. La liste noire de régions n’est pas appliquée : les codes de région doivent correspondre exactement aux valeurs de région signalées par les nœuds (la casse est prise en compte) ; une saisie incorrecte ne génère pas d’erreur, mais aucun filtrage ne sera effectué.
