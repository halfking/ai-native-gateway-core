# Explications des données de la page de gestion marketing

Cette page permet de gérer quatre types d’outils marketing : les forfaits, les packs de recharge, les codes promo et les campagnes.

## Quatre onglets

- **Forfait** : définition de l’abonnement mensuel (quota d’abonnement inclus et prix appliqué).
- **Pack de recharge** : pack de quota supplémentaire acheté en une seule fois.
- **Code promo** : code que les clients échangeient contre une remise ou des points.
- **Campagne** : stratégie de remise ou de bonus à durée limitée (brouillon → actif → terminé).

## Règles d’activation

- **Campagne de remise** : après qu’un client a réclamé la campagne, la remise proportionnelle est appliquée automatiquement à sa prochaine commande ; l’offre reste valable jusqu’à épuisement.
- **Campagne de bonus** : les points sont attribués dès la réclamation et crédités immédiatement.
- Une campagne au statut « brouillon » n’affecte aucune commande ; au statut « actif », elle s’applique aux clients qui l’ont réclamée ; au statut « terminé », elle ne peut plus être réclamée, mais les avantages déjà réclamés restent utilisables jusqu’à épuisement, conformément à leurs règles respectives.

## Comment vérifier les écarts lorsque les chiffres ne correspondent pas

1. Le code promo indique « réclamé N fois », mais la commande ne bénéficie d’aucune remise : la remise s’applique uniquement à la **prochaine** commande après la réclamation ; les commandes antérieures ne sont pas concernées.
2. Les points bonus n’ont pas été crédités : vérifiez que le statut de la campagne est « actif » et non « brouillon », et que le client figure dans la liste des clients ayant réclamé la campagne.
3. Le prix du forfait diffère de celui indiqué sur la page des prix de vente : le forfait est un « prix groupé », distinct du tarif fondé sur le Token ; le quota des clients disposant d’un forfait est déduit du forfait, sans facturer le Token à l’unité.
