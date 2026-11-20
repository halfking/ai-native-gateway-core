# Explication des données de la page du journal d’audit

Cette page enregistre « qui a modifié quoi et quand » : l’historique détaillé des opérations d’administration, permettant une analyse à posteriori.

## Chaque enregistrement contient

L’auteur de l’opération, l’horodatage, le type d’action (par exemple, modifier une configuration, désactiver une clé ou ajuster un paramètre), la cible de l’opération et les valeurs avant et après la modification.

## Portée des enregistrements

- Seules les **opérations d’administration** sont enregistrées (opérations d’écriture effectuées depuis les pages d’administration et les interfaces d’exploitation) ; les appels aux modèles par les utilisateurs ordinaires ne sont pas consignés ici. Consultez la page consacrée aux journaux des requêtes.
- Les opérations de lecture (consultation uniquement, sans modification) ne sont pas enregistrées par défaut.

## Techniques de recherche

- Le filtrage par « auteur de l’opération + période » est le plus efficace : identifiez d’abord la personne, puis resserrez la période.
- Les valeurs avant et après la modification constituent la référence la plus fiable pour vérifier « quand et par qui une configuration a été modifiée ».

## Comment vérifier les écarts entre les données

1. Vous avez modifié une configuration, mais aucun journal n’apparaît : vérifiez que l’opération est passée par l’interface d’administration/l’API d’administration (canal doté de points de suivi d’audit). Une modification directe de la base de données sans passer par la passerelle ne laisse aucune trace.
2. L’horodatage présente un écart de plusieurs heures : les heures des journaux sont en temps universel (UTC) ; convertissez vous-même l’heure locale (l’heure de Pékin est UTC+8).
3. Deux entrées correspondent à la même action : chaque sous-opération est enregistrée séparément (par exemple, une « mise à jour de la configuration » peut être répartie en une entrée « lecture de l’ancienne valeur » et une entrée « écriture de la nouvelle valeur »). Retenez l’entrée la plus complète.
