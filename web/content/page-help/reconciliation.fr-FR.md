# Explication des données de la page de rapprochement

Cette page est l’outil « de rapprochement des comptes » : chaque nuit, elle rassemble les transactions de la veille sur une page et permet de les rapprocher sous plusieurs angles : fournisseur / locataire / personnel / modèle.

## Comment sont générés les instantanés

- La tâche d’agrégation quotidienne génère un instantané **du jour précédent** dans la nuit ; les données du jour ne sont disponibles dans un instantané qu’au petit matin du lendemain. La page affiche le message « L’instantané du jour sera généré au petit matin du lendemain ».
- Si une date ne dispose d’aucun instantané (par exemple, si le système était hors service à ce moment-là), elle est marquée « Non agrégé » ; vous pouvez lancer manuellement un rattrapage jusqu’à une « date de fin de relance ».

## Deux bases de calcul des montants (non cumulées)

- **Coût fournisseur (USD)** : montant payé par la plateforme aux prestataires en amont.
- **Crédits internes / Montant interne** : base de facturation appliquée par la plateforme à ses locataires.
- La différence entre les deux correspond à la marge brute de la plateforme ; la page les présente séparément et ne fait pas de total entre des unités différentes.

## Les quatre postes de Token

Entrées (volume de texte envoyé au modèle), sorties (volume de texte produit par le modèle), lectures du cache (réutilisation des entrées précédentes, sans refacturation ou avec remise), écritures du cache (entrées enregistrées pour la première fois dans le cache). La somme des quatre postes＝le nombre total de Token.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Un instantané est un « état final des comptes » : il est normal que ses chiffres diffèrent de ceux du tableau de bord en temps réel — l’instantané est figé au petit matin du lendemain et ne change plus ensuite ; le tableau de bord en temps réel inclut les données du jour.
2. Les dates marquées « Non agrégé » ne comportent aucun chiffre : lancez un recalcul, puis comparez à nouveau.
3. Lorsque vous rapprochez les données du point de vue d’un fournisseur de sa facture disponible dans son espace d’administration, vérifiez le fuseau horaire : ce système délimite les journées selon UTC, tandis que la facture du fournisseur peut être établie sur son fuseau horaire local ; le jour auquel est rattaché le trafic traversant minuit peut donc différer.
4. Les requêtes en échec génèrent également de petits frais (par exemple, une réponse partielle déjà facturée par le prestataire en amont) ; les jours où le taux d’échec est élevé présentent un ratio entre les coûts et le nombre de requêtes plus élevé.
