# Explication des données de la page de rapprochement

Cette page récapitule l’usage enregistré par la passerelle la veille, selon plusieurs vues : fournisseur, locataire, personne, modèle, etc. Elle sert à rapprocher les enregistrements de la passerelle ; ce n’est ni une facture fournisseur ni un montant dû confirmé.

## Comment sont générés les instantanés

- La tâche d’agrégation quotidienne génère un instantané **du jour précédent** dans la nuit ; les données du jour ne sont disponibles dans un instantané qu’au petit matin du lendemain. La page affiche le message « L’instantané du jour sera généré au petit matin du lendemain ».
- Si une date ne dispose d’aucun instantané (par exemple, si le système était hors service à ce moment-là), elle est marquée « Non agrégé » ; vous pouvez recalculer les données manquantes jusqu’à une « date de fin de relance ». Les instantanés existants peuvent aussi être recalculés : ils ne sont pas définitifs de façon permanente.

## Deux bases de calcul des montants (non cumulées)

- **Montant côté fournisseur** : somme des usages à l’état terminal enregistrés par la passerelle, y compris les usages sans fournisseur attribué et le trafic non professionnel. Il s’agit d’une estimation enregistrée par la passerelle, pas d’un montant renvoyé par le fournisseur ni d’un montant dû ou payé confirmé.
- **Crédits internes / Montant interne** : trafic professionnel uniquement ; les crédits sont convertis selon le facteur de prix enregistré avec l’instantané. Il s’agit de la base de facturation interne de la plateforme, et non d’une preuve de facturation ou d’encaissement.
- La comparaison n’est pertinente que si le périmètre du trafic et la devise correspondent. La différence ne représente pas automatiquement la marge brute. La page les présente séparément et ne fait pas de total entre des unités différentes.

## Les quatre postes de Token

Ce rapport additionne actuellement les entrées, les sorties, les lectures et les écritures du cache. Les valeurs du cache peuvent recouper celles des entrées. Pour comparer les rapports, additionnez les entrées normalisées et les sorties, puis affichez les lectures et écritures du cache séparément.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Un instantané est un agrégat calculé à partir des données disponibles à ce moment-là. Les écarts avec le tableau de bord en temps réel sont normaux : celui-ci peut inclure les données du jour, et l’instantané peut être actualisé en relançant l’agrégation.
2. Les dates marquées « Non agrégé » ne comportent aucun chiffre : lancez un recalcul, puis comparez à nouveau.
3. Pour comparer la vue fournisseur à une facture, vérifiez le périmètre autant que le fuseau horaire : cette page additionne les usages à l’état terminal enregistrés par la passerelle, y compris ceux sans attribution et le trafic non professionnel, et présente une estimation plutôt que le montant facturé par le fournisseur. La facture peut aussi utiliser des jours locaux plutôt qu’UTC.
4. Les requêtes en échec pour lesquelles un usage terminal ou un coût est enregistré sont incluses dans l’agrégat de la passerelle ; cela ne signifie pas que le fournisseur a confirmé une facturation.
