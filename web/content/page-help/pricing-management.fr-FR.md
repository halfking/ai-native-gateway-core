# Explication des données de la page de gestion des tarifs

Cette page gère le « tableau des coûts » — il indique ce que coûte un appel en amont pour chaque modèle et constitue la base du calcul des coûts et de la tarification interne.

## Explication des vues

- **Vue arborescente** : permet de parcourir les données selon la hiérarchie « Fournisseur → Modèle », pratique pour vérifier fournisseur par fournisseur.
- **Vue tableau** : tous les tarifs sont disposés à plat dans un tableau unique, pratique pour les contrôles et les exports en masse.

## Méthode de calcul des prix

- Les prix sont exprimés par « million de Token » : le prix des entrées et celui des sorties sont indiqués séparément, le prix de sortie étant généralement plus élevé.
- La part des entrées servie depuis le cache est facturée à tarif remisé (10% pour certains fournisseurs) ; les modèles disposant d’un prix de cache disposent d’une ligne distincte.
- Les changements de prix ne concernent que les appels **réalisés après leur date d’entrée en vigueur** ; les factures historiques restent figées selon le tarif applicable à l’époque et ne font l’objet d’aucun recalcul rétroactif.

## Comment vérifier les écarts entre les chiffres

1. Si le coût et la facture du fournisseur présentent un écart au niveau d’une décimale : vérifiez l’unité de tarification (par million de Token contre par mille de Token) ; les deux valeurs diffèrent d’un facteur 1000.
2. Deux prix sont affichés pour un même modèle : l’un correspond au prix standard et l’autre au prix du cache, ou les deux lignes correspondent à des intégrations auprès de fournisseurs différents — vérifiez d’abord la colonne « Fournisseur ».
3. Le rapport du jour n’a pas changé après une modification du prix : les rapports sont figés par jour et seul l’instantané du lendemain reflète le nouveau prix.
4. Écart de taux de change : le taux configuré dans la plateforme est utilisé pour convertir les coûts en USD et les afficher en RMB ; un écart avec le taux banque en temps réel est normal.
