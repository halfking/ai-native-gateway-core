# Explication des données de la page de surveillance de l’intégrité des modèles

Cette page vérifie si « les réponses fournies en amont sont complètes et n’ont pas été remplacées » : les substitutions, troncatures, réponses vides, répétitions et dérives d’empreinte constituent autant d’anomalies d’intégrité.

## Deux onglets

- **Événements d’intégrité** : chaque anomalie fait l’objet d’un enregistrement (type, gravité, modèle/fournisseur concerné, état de résolution).
- **Dérive d’empreinte** : évolution au fil du temps de « l’empreinte comportementale » d’un même modèle — une dérive importante indique que l’implémentation en amont a été modifiée sans que cela soit signalé.

## Règles de calcul

- Le nombre total d’événements, le nombre d’événements non résolus et le nombre d’événements graves sont calculés sur la fenêtre statistique sélectionnée ; celle-ci peut être modifiée.
- « Grave » = une anomalie qui affecte l’exactitude ou la sécurité de la réponse (par exemple, une substitution de modèle) ; une « troncature/réponse vide » est généralement de gravité moyenne.
- La dérive d’empreinte est une comparaison **par rapport à la référence** : après une mise à jour de la référence, les conclusions relatives aux dérives historiques sont recalculées.

## Comment vérifier les écarts numériques

1. Le nombre d’événements diffère de celui de la page des anomalies de format : les deux pages ciblent des types d’anomalies différents (cette page couvre « l’intégrité des réponses », l’autre « le format des réponses ») ; il y a des chevauchements, mais les ensembles ne coïncident pas.
2. Le nombre d’événements « non résolus » ne diminue pas : leur traitement nécessite une confirmation humaine ; comme sur la page des anomalies de format, un rétablissement automatique ne clôt pas automatiquement le dossier.
3. Toutes les empreintes dérivent soudainement : vérifiez d’abord si la référence vient d’être mise à jour ou si la fenêtre statistique a changé, avant de soupçonner une refonte générale de l’implémentation en amont.
