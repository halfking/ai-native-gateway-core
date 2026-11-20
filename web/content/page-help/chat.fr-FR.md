# Explication des données de la page de conversation

Cette page est une « fenêtre de test de conversation » intégrée : elle permet d'échanger directement avec le modèle connecté à la passerelle et constitue le chemin le plus court pour vérifier qu'une clé fonctionne.

## Prérequis

Il vous faut une **clé API qui vous appartient et qui est activée**. Sans clé, la page vous invite d'abord à en demander une ; si la clé est désactivée ou expirée, la conversation échoue en indiquant la raison.

## Choix du modèle

- Avec « auto », la passerelle sélectionne automatiquement le modèle adapté (selon la stratégie de routage).
- Lorsque vous Specifyz manuellement un modèle, votre choix est respecté ; si ce modèle n'est pas disponible, le tour de conversation en cours échoue, sans impact sur le tour suivant.
- **Les modifications des paramètres prennent effet au tour suivant** : changer la température ou de modèle s'applique à partir du message suivant ; les messages historiques restent inchangés.

## Correspondance avec les statistiques

- Chaque tour de conversation ici correspond à un appel réel, **il est comptabilisé dans la consommation et les frais** — les tests coûtent aussi de l'argent.
- Les conversations correspondent une à une aux enregistrements de la page « Historique des requêtes » : le détail de chaque tour est consultable dans les enregistrements de requêtes.

## Que vérifier quand les chiffres ne concordent pas

1. Deux tours de la même conversation affichent des frais très différents : en mode « auto », le modèle est choisi indépendamment à chaque tour ; il a pu basculer vers un modèle plus cher ou moins cher.
2. La réponse est tronquée : vérifiez d'abord le paramètre « Longueur maximale de la sortie », puis la capacité maximale du modèle sélectionné.
3. La conversation échoue alors que la page de la clé indique un état normal : vérifiez que le quota de la clé n'est pas épuisé et que le modèle fait partie des modèles disponibles pour votre clé.
