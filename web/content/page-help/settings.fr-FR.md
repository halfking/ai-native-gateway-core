# Explication des données de la page des paramètres système

Cette page constitue le « panneau de contrôle principal » de l’ensemble de la plateforme : le mode d’interface, le comportement de compression, les options de sécurité et les autres paramètres globaux y sont tous configurés.

## Mode d’application des paramètres

- La plupart des paramètres sont **appliqués dès l’enregistrement** (la prochaine requête utilise les nouvelles valeurs).
- Les paramètres signalés par « Nécessite un redémarrage » doivent attendre le redémarrage du service ; la page affiche un message distinct pour ce type de paramètres.

## Modes simplifié/expert

- « Simplifié » : la page d’accueil présente la liste des connexions et toutes les fonctionnalités sont regroupées sous « Avancé » — pour les personnes qui les utilisent rarement.
- « Expert » : navigation complète et accès à toutes les pages.
- Il s’agit uniquement d’un changement de **projection de l’interface** : les deux modes affichent les mêmes données et utilisent les mêmes règles de calcul ; les chiffres ne varient pas selon le mode.

## Mode de compression (traitement des conversations très longues)

- Mode 1 (anticipation) : compression proactive lorsque l’on approche de la limite de capacité du modèle.
- Mode 2 (reactif, recommandé) : compression et nouvelle tentative automatique après réception d’une erreur « conversation trop longue » — compression uniquement lorsque nécessaire, afin d’économiser les ressources.
- La compression affecte uniquement **l’organisation du contenu envoyé au modèle**, sans supprimer le texte original de la conversation ; les statistiques et la facturation sont calculées selon les appels réellement effectués.

## Comment vérifier en cas d’écart entre les chiffres

1. Les paramètres ont été modifiés, mais le comportement n’a pas changé : vérifiez d’abord si le paramètre est signalé par « Nécessite un redémarrage », puis si un message de confirmation de l’enregistrement s’affiche.
2. Les chiffres diffèrent entre les deux modes : cela est impossible — si vous constatez une différence, les périodes ou les filtres utilisés de part et d’autre sont différents ; alignez d’abord les périodes.
3. Une option est activée, mais certaines requêtes ne l’utilisent pas : certaines options s’appliquent par locataire ou par clé, tandis que les options globales ne servent que de solution de repli. Pour une requête donnée, la configuration au niveau le plus fin prévaut.
