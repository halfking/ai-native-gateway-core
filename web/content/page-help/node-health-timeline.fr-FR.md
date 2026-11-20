# Page de chronologie de l’état de santé des nœuds

Chronologie de l’état de santé d’une paire d’identifiants : quand elle fonctionne, quand elle ne fonctionne pas et quand elle se rétablit.

## Explication des règles

Axe horizontal = temps, bloc de couleur = état ; la chronologie est uniquement ajoutée et jamais modifiée. Elle constitue la source de référence pour追溯 l’état de santé des identifiants.

## Comment vérifier en cas d’écart entre les chiffres

- 1. La chronologie présente des interruptions : les interruptions de collecte ne sont pas comblées rétroactivement (fenêtre de redémarrage du service) ; une interruption ≠ un fonctionnement normal.
- En cas d’incohérence avec l’état indiqué sur la page de supervision : le bloc de couleur le plus récent de la chronologie fait foi ; la page de supervision est peut-être mise en cache.
