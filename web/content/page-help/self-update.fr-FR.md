# Explication des données de la page de mise à jour

Cette page gère les mises à jour de version de la passerelle elle-même : recherche de la dernière version → alerte → installation après confirmation par un administrateur.

## Trois états

- **Source de versions non configurée** : l’adresse à interroger pour rechercher les nouvelles versions n’est pas renseignée sur ce nœud ; la vérification automatique n’est pas activée.
- **État de version inconnu** : la recherche a été effectuée, mais aucun résultat n’a été obtenu (problème réseau ou source inaccessible).
- **Normal** : la version actuelle et la dernière version sont connues et peuvent être comparées.

## Processus de mise à jour

1. « Vérifier les mises à jour maintenant » interroge la source de versions configurée afin de récupérer le numéro de la dernière version.
2. Lorsqu’une nouvelle version est disponible, une alerte est générée ; **l’installation n’est pas automatique**.
3. Après confirmation de l’installation par un administrateur, le processus de mise à jour commence. Une fois terminé, le service redémarre automatiquement avec la nouvelle version.

## Règles d’interprétation

- La « version actuelle » correspond à la version **exécutée sur ce nœud** ; lors d’un déploiement multinœud, les nœuds peuvent exécuter des versions différentes. Cette page ne présente que les données de ce nœud.
- Pendant la mise à jour, le service redémarre brièvement : les requêtes sont prises en charge par les autres nœuds (déploiement multinœud) ou sont brièvement interrompues (déploiement à un seul nœud).

## Comment vérifier les écarts de données

1. Après une mise à jour, l’ancienne version s’affiche encore : les informations de version mises en cache doivent être actualisées une fois pour que l’affichage soit mis à jour.
2. La « dernière version » ne correspond pas à la page des versions publiées officiellement : cette page lit la **source de versions configurée**. Si celle-ci n’est pas synchronisée avec la source officielle, elle fait foi. Vérifiez d’abord l’adresse de la source configurée.
3. Le résultat de la recherche de mises à jour reste « inconnu » : l’adresse de la source de versions est inaccessible ; testez directement la connectivité à cette adresse depuis la machine de déploiement.
