# Explication des données de la page des prix de marché

Cette page gère les « données de marché externes » : elle collecte les prix de marché des modèles auprès de différentes plateformes de marché, afin de permettre la comparaison des prix et de servir de référence à la fixation des prix. Cette page assure une **réconciliation en lecture seule + gestion des métadonnées** ; elle ne fixe pas directement les prix.

## Deux rubriques

- **Registre des plateformes** : indique les plateformes de marché dont les données sont collectées. Les plateformes marquées « Interne » ou « Désactivée » ne participent ni à la synchronisation des données de marché ni au calcul du prix plancher de marché.
- **Journal de synchronisation** : historique des 10 dernières exécutions de collecte (date et heure, nombre d’éléments collectés, nombre d’éléments rejetés).

## Identité de réconciliation

À chaque synchronisation, l’identité suivante est respectée : **nombre d’éléments sources = nombre d’éléments enregistrés + nombre d’éléments rejetés**. Les trois chiffres figurent dans le journal. En cas d’écart, le détail des éléments rejetés permet d’en connaître la cause (cas fréquents : devise et région qui ne correspondent pas, doublons).

## Règles d’appariement des régions et des devises

Plateforme internationale ↔ dollar ; plateforme nationale ↔ renminbi. La configuration doit être effectuée par paires ; les éléments dont l’appariement est incorrect seront rejetés lors de la synchronisation.

## Comment vérifier lorsque les chiffres ne correspondent pas

1. La colonne « Rejet » du journal affiche un chiffre en rouge : cela correspond au fonctionnement attendu du mécanisme de réconciliation ; ouvrez le détail de la synchronisation concernée pour connaître les motifs précis de rejet.
2. Le prix de marché diffère du prix que vous constatez : les données de marché sont collectées selon une périodicité et ne sont pas en temps réel ; lors de la comparaison, vérifiez la « date de synchronisation ».
3. Une plateforme n’a toujours aucune donnée : vérifiez dans le registre si elle est désactivée ou si le synchroniseur n’a encore jamais été exécuté pour cette plateforme.
