# Page des stratégies de routage

Paramètres de la stratégie de sélection des routes : ordre des niveaux, limite de candidats, règles de repli.

## Définition des termes

La stratégie détermine « comment sélectionner » ; les données candidates déterminent « quoi sélectionner ».

## Comment vérifier les écarts entre les chiffres

- 1. La stratégie a été modifiée, mais le trafic n’a pas changé : la stratégie ne s’applique qu’aux nouvelles requêtes ; les requêtes en cours sont traitées selon l’ancienne stratégie.
- Comportement anormal après inversion de l’ordre des niveaux : les niveaux sont interdépendants. Après ajustement, effectuez d’abord une simulation de validation sur la page de décision.
