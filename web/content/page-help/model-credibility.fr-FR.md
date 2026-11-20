# Explication des données de la page « Crédibilité des modèles »

Cette page détecte si le fournisseur en amont « fait passer un modèle de gamme inférieure pour un modèle de gamme supérieure » — ce que l’on appelle couramment un modèle « gonflé » ou « dégradé ». Quatre repères sont utilisés en validation croisée ; s’il est impossible de conclure, le statut « Preuves insuffisantes » est appliqué sans être assimilé à l’absence de problème.

## Les quatre conclusions

| Conclusion | Signification |
| --- | --- |
| Vérifié | Plusieurs éléments de preuve concordent : le modèle correspond bien au niveau annoncé |
| Suspect | Des signaux suspects sont présents, mais insuffisants pour conclure ; une vérification est recommandée |
| Gonflé | Des éléments de preuve solides indiquent que le service réellement fourni est de niveau inférieur à la configuration annoncée |
| Preuves insuffisantes | Trop peu de preuves pour déterminer la situation (**cela ne signifie pas qu’il n’y a pas de problème**) |

## Les quatre repères (critères de décision)

L’identité déclarée par le fournisseur en amont, la comparaison des capacités de modèles équivalents, la dérive de la signature des requêtes et l’étalonnage par rapport à une référence externe.

## Explication des règles de calcul

- Les « nœuds évalués » sont comptés par combinaison « Modèle × fournisseur » ; un même modèle est évalué séparément pour chaque fournisseur.
- La conclusion repose sur la **dernière détection** ; une « détection complète » relance l’ensemble des analyses et remplace les anciennes conclusions.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Le statut « Suspect » est fréquent, mais l’activité ne présente aucune anomalie : ce statut ne constitue pas une preuve de faute, mais un signal inviting à vérifier à nouveau ; examinez-le conjointement avec la dérive des empreintes observée sur la page de surveillance de l’intégrité.
2. Le statut passe à « Preuves insuffisantes » juste après une détection : cette combinaison a récemment généré trop peu de trafic, ce qui rend l’échantillonnage insuffisant ; relancez la détection une fois qu’un trafic réel est disponible.
3. Les capacités ne correspondent pas à celles affichées dans le back-office du fournisseur : parmi les quatre repères, la « comparaison des capacités » dépend du trafic réel mesuré sur notre plateforme ; les modèles peu utilisés disposent naturellement de moins d’éléments de preuve.
