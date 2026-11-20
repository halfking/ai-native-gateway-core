# Page de cascade de planification

Représentez le processus de planification de chaque requête sous forme de cascade : les candidats sont évalués un par un, ce qui permet de voir clairement qui a pris en charge la requête et qui a échoué.

## Explication des règles de calcul

La longueur de la barre horizontale correspond au temps de traitement de ce candidat ; l’étiquette de réussite ou d’échec est placée au bout de la barre ; chaque requête ne comporte qu’une seule ligne de réussite finale.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

- 1. Le nombre de barres de la cascade est supérieur au nombre de candidats : des nouvelles tentatives internes sont incluses ; utilisez la ligne « Réussite finale » comme référence.
- Le temps total diffère de celui indiqué dans le détail de la requête : la cascade ne couvre que la phase de planification et n’inclut pas le temps de génération du modèle.
