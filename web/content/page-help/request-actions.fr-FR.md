# Page de chronologie des actions de requête

Les actions de chaque requête dans la passerelle sont présentées par ordre chronologique (avec une granularité au niveau de l’action, plus fine que celle de l’arbre des étapes).

## Explication du périmètre

Une action correspond à une opération interne précise (lire ou écrire dans le cache, appliquer des règles, envoyer une requête en amont) ; plusieurs actions peuvent s’exécuter simultanément.

## Comment vérifier lorsque les chiffres ne correspondent pas

- 1. Le nombre d’actions diffère du nombre d’étapes : une étape contient plusieurs actions, la granularité est donc différente.
- L’intervalle entre les horodatages est de 0 : les actions inférieures à la milliseconde sont regroupées à l’affichage, et non perdues.
