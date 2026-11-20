# Explication des données de la page de tarification des modèles standard

Cette page définit le « prix de vente » — le montant que l’utilisateur paie pour chaque utilisation d’un modèle standard. Elle est associée à la page « Prix de revient » : le prix de revient correspond au prix d’achat, tandis que cette page indique le prix de vente.

## Règles de tarification (trois règles)

- **Une ligne par modèle standard** : les utilisateurs s’intéressent uniquement au « montant payé pour utiliser ce modèle », sans se soucier du fournisseur utilisé en arrière-plan.
- **Prix de référence global × coefficient de remise** : les dimensions sans tarification distincte (entrée/sortie/cache) sont calculées automatiquement en multipliant le prix de référence global par la remise.
- **La tarification manuelle est prioritaire** : les champs cochés « Manuel » sont figés et les réajustements globaux des prix ne les affectent pas. Ce mécanisme permet de préserver le prix de modèles particuliers.

## Facturation multimodale

Le volume de texte des modèles d’image, d’audio et de vidéo est facturé séparément selon son **tarif dédié**, sans être fusionné avec le texte ordinaire. Les lignes des modèles comportant une tarification multimodale portent la mention « Tarification multimodale incluse ».

## Cohérence avec les autres pages

- Les changements effectués sur cette page **sont appliqués immédiatement** à la facturation des appels ultérieurs ; les tendances du jour et les factures ne refléteront ces changements qu’après le prochain instantané.
- Les prix des « Forfaits et recharges » affichés côté utilisateur proviennent de cette page. En cas de divergence, cette page fait foi et la tâche de synchronisation doit être vérifiée.

## Comment vérifier les écarts entre les chiffres

1. Après un réajustement des prix, l’affichage côté utilisateur n’a pas changé : vérifiez si le cache côté utilisateur n’a pas été actualisé ou si le champ du modèle est coché « Manuel ».
2. Un même modèle affiche deux prix de vente : l’un peut être calculé avec la remise globale et l’autre avec une tarification manuelle — consultez la mention « Manuel ».
3. Le montant facturé ne correspond pas au journal des appels : confirmez l’ordre entre le moment de la valorisation et celui du réajustement des prix. Une facture à cheval sur un réajustement est calculée par tranches.
