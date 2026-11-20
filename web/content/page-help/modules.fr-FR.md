# Page de gestion des modules

Panneau de contrôle des modules fonctionnels de la plateforme : chaque module peut être activé ou désactivé indépendamment.

## Définition des règles

Statut du module = activé/désactivé ; l’entrée des modules désactivés est masquée et la configuration est conservée.

## Comment vérifier en cas d’écart entre les chiffres

- 1. Le menu reste affiché après la désactivation : le masquage de l’entrée peut être retardé par le cache ; il disparaît après actualisation.
- Le module est activé, mais la fonctionnalité est indisponible : il se peut que le service sous-jacent dont dépend le module ne soit pas prêt ; consultez l’état des dépendances dans les détails du module.
