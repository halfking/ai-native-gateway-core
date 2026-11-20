# Page de configuration des approbations

Définir les opérations nécessitant une approbation manuelle : conditions de déclenchement, approbateurs et stratégie d’expiration.

## Explication des règles

Stratégie = condition + approbateur + expiration, soit trois éléments ; toute modification de la configuration ne s’applique qu’aux approbations déclenchées ultérieurement.

## Comment vérifier les écarts en cas d’incohérence des chiffres

- 1.Une approbation est configurée mais n’est pas générée : vérifier la portée des conditions de déclenchement (niveau tenant/clé) et l’état de l’activation.
- L’approbation/le rejet automatiques après expiration ne fonctionne pas : une valeur d’expiration de 0 signifie une attente illimitée, sans clôture automatique.
- L’approbateur ne reçoit pas la notification : vérifier que le compte de l’approbateur est toujours activé.
