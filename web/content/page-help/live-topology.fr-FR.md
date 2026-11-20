# Explication des données de la page de topologie en temps réel

Cette page présente la « carte de trafic en temps réel » de la passerelle : quels nœuds sont en ligne et dans quelles directions les requêtes circulent actuellement.

## Éléments affichés sur le graphique

- **Nœud** = un point de service capable de traiter des requêtes (nœud d’accès, nœud fournisseur).
- **Liaison** = une ligne reliant deux nœuds ; l’épaisseur ou le nombre de traits indique le volume du trafic.
- Une requête entre par le « côté utilisateur », son itinéraire est sélectionné par la passerelle, puis elle ressort par une « liaison fournisseur » afin d’obtenir une réponse.

## État des nœuds

- Les statuts En ligne/Hors ligne sont déterminés par le heartbeat en temps réel des nœuds ; en cas d’anomalie Redis, l’état de santé est déclassé et affiché « Inconnu » — cela indique un problème du canal de collecte, pas nécessairement une indisponibilité réelle du nœud.
- Le badge « Connexion directe interne » indique que ce nœud fournisseur est directement connecté à la plateforme et géré par elle, sans passer par un proxy tiers.

## Fréquence des mises à jour

- Les mises à jour sont poussées via une connexion persistante ; en fonctionnement normal, l’actualisation se fait à la fréquence de la seconde.
- En cas de coupure brève, la page se reconnecte automatiquement ; les requêtes émises pendant la coupure ne sont pas perdues, l’affichage se met simplement à jour avec un léger retard.
- « N requêtes transitant par des nœuds non valides sont masquées » = les requêtes jugées comme passant par des nœuds non valides ne sont pas affichées par défaut sur les liaisons ; vous pouvez activer l’interrupteur d’affichage pour les voir.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. L’affichage en temps réel est un instantané « du moment présent » ; il n’est pas directement comparable aux cumuls de la page Statistiques pour « une période passée ». Utilisez la page Registre des requêtes pour le rapprochement.
2. Les chiffres sur les liaisons correspondent au nombre de requêtes ayant circulé dans une fenêtre glissante. Cette fenêtre avance dans le temps, si bien que les chiffres diminuent naturellement.
3. Si une requête semble rester bloquée sur une liaison, commencez par consulter son étiquette d’état : En cours/Terminée/Échec correspond à différentes étapes du traitement.
4. Si tous les nœuds affichent « Inconnu », il s’agit le plus souvent d’une défaillance du canal de messages. Actualisez la page ou attendez la reconnexion pour rétablir l’affichage.
