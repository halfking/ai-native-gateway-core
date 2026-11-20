# Page de décision de routage

Journal du résultat final de sélection du chemin de routage pour chaque requête (qui a été sélectionné et pourquoi).

## Explication des indicateurs

Une ligne = une décision ; elle contient la chaîne de règles correspondantes et le candidat final.

## Comment vérifier en cas d’écart entre les chiffres

- 1.Nombre de décisions ≠ nombre de requêtes : les requêtes bloquées en amont ne sont pas soumises au routage et ne génèrent aucun enregistrement de décision.
- Deux décisions pour une même requête : l’appel principal et la tentative de réessai génèrent chacun une décision.
