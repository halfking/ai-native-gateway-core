# Détails du parcours de la requête

Consultez le « parcours » d’une requête au fil de plusieurs tentatives : l’entrée, la sortie et le résultat de chaque tentative candidate sont reliés pour former une chaîne complète.

## Définitions

Parcours = requête principale + toutes les tentatives de relance ; durée totale = différence entre le premier et le dernier horodatage, incluant toutes les tentatives.

## Comment vérifier les écarts lorsque les chiffres ne correspondent pas

- 1.Nombre de parcours ≠ nombre de tentatives candidates : les relances internes et les replis sont inclus ; la facturation est fondée sur la tentative ayant finalement abouti.
- Tentatives interrompues en cours de route : les tentatives ayant expiré côté service en amont sont marquées « Interrompue », et leurs frais partiels sont déterminés selon les règles de facturation réelles du service en amont.
