# Explication des données de la page « Cycle de vie des données »

Cette page gère le « stockage, le déplacement et le nettoyage » des données : occupation de l’espace par les données de la base, dates d’archivage et modalités de récupération.

## Aperçu des onglets

- **Vue d’ensemble du stockage** : espace occupé par chaque table et vitesse de croissance.
- **Migration des tables Hot & nettoyage des partitions** : répartir les grandes tables à forte fréquence d’écriture en partitions mensuelles et archiver entièrement les mois les plus anciens.
- **Reprise après dégradation et nettoyage** : reconstruire les données agrégées à partir des données détaillées en cas de corruption.
- **Configuration du stockage** : paramètres de connexion à la base de données et de emplacement du stockage.
- **Gestion des journaux** : conservation et nettoyage des journaux d’exécution.
- **Pièces jointes de session / grands champs / système de fichiers** : occupation et maintenance des données de type fichier.

## Points de référence essentiels

- **Durée de conservation** : période pendant laquelle les données restent dans la base ; une fois ce délai atteint, elles deviennent « éligibles au nettoyage ». Le nombre de lignes affectées peut être prévisualisé avant le nettoyage.
- « Éligibles au nettoyage » ne signifie pas « supprimées » : le nettoyage nécessite une prévisualisation puis une exécution manuelle. Si de nouvelles données sont écrites entre la prévisualisation et l’exécution, le nombre de lignes réellement traitées peut légèrement différer de celui indiqué.
- L’espace occupé par les tables augmente d’abord, puis diminue : pendant les opérations de réorganisation (récupération d’espace), l’espace utilisé sur le disque double dans un premier temps (fichiers anciens et nouveaux coexistant). Il n’est libéré qu’à la fin de l’opération.

## Mécanismes de sécurité

- Toutes les actions de nettoyage nécessitent une « prévisualisation » avant l’« exécution » : ces deux étapes sont distinctes.
- L’archivage consiste à déplacer les données vers un stockage plus lent, et non à les supprimer. Les données archivées restent récupérables.

## Comment vérifier les écarts entre les chiffres

1. Les chiffres d’occupation proviennent des statistiques de la base de données et présentent toujours un écart avec la taille des fichiers observée dans le système d’exploitation, en raison de l’espace de redondance interne de la base. Il suffit que l’ordre de grandeur soit cohérent.
2. L’augmentation progressive du nombre de lignes « éligibles au nettoyage » est normale, car elle correspond au vieillissement des données, et non à une anomalie de croissance.
3. Les fluctuations du nombre de lignes observées pendant une reconstruction ou une migration correspondent à un état transitoire. L’état final après la fin de la tâche fait foi.
4. Vérifiez d’abord l’onglet : les mesures diffèrent selon l’onglet pour une même table (la vue d’ensemble présente l’espace occupé, tandis que l’onglet des partitions présente la répartition des lignes).
