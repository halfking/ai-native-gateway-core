# Explication des données de la page Modèles et catalogue

Cette page gère le « dictionnaire des modèles » : les modèles pris en charge par la plateforme, leurs noms normalisés et la correspondance avec les appellations de chaque fournisseur.

## Trois onglets

- **Normatif** (visible par les équipes d’exploitation de la plateforme) : liste normalisée des modèles reconnus par la plateforme — les renommages, les catégories, la longueur de contexte et les étiquettes de capacités sont tous gérés ici.
- **Catalogue** : permet de consulter, par fournisseur, leurs modèles d’origine et leurs tarifs.
- **Correspondance des noms** : un même modèle peut porter des noms différents selon les fournisseurs (par exemple, avec un suffixe de version ou de date) ; le tableau de correspondance aligne l’« appellation du fournisseur » sur le « nom normalisé de la plateforme ». Tous les rapports regroupent les données selon le nom normalisé ; plus la correspondance est complète, plus les rapports sont exacts.

## Signification des noms normalisés

- Toutes les statistiques d’utilisation, les rapprochements et les classements utilisent le « nom normalisé » comme clé de regroupement.
- Si le nouveau modèle d’un fournisseur n’a pas encore de correspondance, son trafic apparaît séparément sous son nom d’origine (dans les statistiques, cela se traduit par « un modèle inconnu supplémentaire »). Une fois la correspondance ajoutée, il est automatiquement regroupé sous le nom normalisé.

## Comment vérifier les écarts numériques

1. Un nom de modèle inconnu apparaît dans un rapport : ajoutez d’abord la correspondance dans « Correspondance des noms », puis consultez à nouveau le rapport (les données historiques seront regroupées à nouveau selon le nom normalisé).
2. Il est normal que le même modèle ait des prix différents selon les fournisseurs : le catalogue indique les tarifs séparément pour chaque fournisseur ; le coût calculé dans les statistiques repose sur le tarif du fournisseur effectivement appelé.
3. La longueur de contexte ne correspond pas au site officiel : le paramétrage du catalogue de la plateforme fait référence — c’est lui qui détermine la limitation ou la compression du contexte effectuée par la passerelle.
