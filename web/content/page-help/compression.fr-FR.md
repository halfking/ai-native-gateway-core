# Explication des données de la page de gestion de la compression

Cette page surveille la « réduction de volume » des conversations trop longues : lorsque la conversation est trop longue pour tenir dans la capacité du modèle, elle explique comment le système la compresse.

## Indicateurs clés

- **Nombre de sessions compressées** : nombre de sessions ayant déclenché au moins une compression.
- **Taux de compression** : proportion du contenu supprimée après compression.
- **Répartition des stratégies** : nombre d’utilisations de chaque stratégie de compression (prévisionnelle/réactive/à fenêtre glissante/incrémentale, etc.).
- **Évolution du taux de compression** : variation du taux de compression moyen quotidien.

## Différence entre les deux modes de déclenchement

- **Compression prévisionnelle** (initiale) : compression anticipée lorsque l’estimation indique que la capacité sera bientôt dépassée, sans impact pour l’utilisateur.
- **Compression forcée** : l’amont refuse d’abord la requête (erreur de capacité insuffisante), puis le système compresse et réessaie — cela entraîne le coût d’un appel en échec supplémentaire, mais la compression n’est déclenchée que lorsque nécessaire.

## Périmètre de calcul

- La compression se produit « avant l’envoi au modèle » ; le texte original de la conversation n’est pas modifié. Les statistiques mesurent la proportion de volume envoyé à l’amont qui est supprimée.
- Un tour de conversation peut déclencher plusieurs compressions (réductions successives de volume à mesure que la conversation s’allonge) ; chacune est enregistrée séparément.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Un taux de compression anormalement élevé, proche de 100% : vérifiez si le seuil de compression a été mal configuré (au point de compresser aussi des questions très courtes).
2. Il est normal que le nombre de sessions « compressées » soit beaucoup plus élevé que le nombre de compressions « forcées » : la plupart sont réalisées par compression prévisionnelle, et seules les conversations que la détection a laissées passer subissent une compression forcée.
3. Si la configuration affiche « Valeur par défaut d'usine », un avertissement en haut de la page signale l’échec de lecture de la configuration. Les valeurs affichées plus bas ne correspondent donc pas à la configuration actuellement en vigueur : corrigez d’abord l’échec de lecture avant d’ajuster les paramètres.
