# Page d’état de la migration du stockage

Tableau de bord de l’avancement du transfert des données entre les couches de stockage.

## Définitions des indicateurs

Migration = copie par lots + validation + basculement ; la progression est calculée par lot.

## Comment vérifier les écarts lorsque les chiffres ne correspondent pas

- 1. Progression bloquée : le déplacement d’une grande table par lot prend du temps ; consultez le détail des lots plutôt que le pourcentage global.
- Échec de validation : un lot en échec peut être relancé, sans impact sur les lots déjà terminés.
