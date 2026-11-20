# Page d’audit de la réécriture du routage

Journal d’audit indiquant qui a modifié la configuration du routage et à quel moment.

## Périmètre des données

Chaque enregistrement contient les valeurs avant et après la modification ainsi que l’opérateur ; les enregistrements sont uniquement ajoutés, jamais modifiés.

## Comment vérifier en cas d’écart entre les chiffres

- 1. Une modification est introuvable : si elle a été effectuée directement en base de données sans passer par la page, aucun audit n’est conservé ; toute opération effectuée via la page est forcément journalisée.
- Le comportement reste inchangé après la modification : vérifiez la plage de trafic réellement concernée par cette règle.
