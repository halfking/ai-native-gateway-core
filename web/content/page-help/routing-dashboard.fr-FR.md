# Explication des données de la page de vue d’ensemble du routage

Cette page répond à la question « Pourquoi une requête a-t-elle été attribuée à un modèle ? » en vous montrant les différentes étapes du processus de routage.

## Concepts clés

- **Candidat** = identifiant sélectionné selon des critères et pouvant prendre en charge cette requête.
- **Routable** = partie des candidats actuellement réellement utilisable (en bon état, non désactivée et hors période de refroidissement).
- **Journal des décisions** = flux des résultats de routage pour chaque requête réelle.

## Règles de calcul

- Le « nombre de candidats routables » est évalué à l’instant présent : un même candidat peut être disponible une minute plus tôt et entrer en refroidissement une minute plus tard ; le nombre évolue en conséquence.
- Les détails des décisions listent les candidats par « niveau » : ceux qui figurent en priorité traitent la requête en premier ; en cas d’échec, le système passe au suivant dans l’ordre.
- Le « repli » = lorsqu’un candidat préféré échoue, le candidat suivant prend le relais ; une requête peut avoir au plus un seul enregistrement de réussite finale.

## Description des onglets

- Décision en temps réel : lancez une simulation pour voir « comment une requête serait routée maintenant ».
- Surveillance des identifiants / Sonde de santé : détails de la disponibilité des identifiants en amont.
- Exploitation automatisée : outils de réglage et d’analyse gérés automatiquement par le système.

## Comment vérifier en cas d’écart entre les chiffres

1. Si le résultat de la simulation diffère de l’enregistrement réel : la simulation est une « projection à l’instant présent », tandis que la décision réelle a été prise dans le passé ; l’état de refroidissement et les désactivations temporaires peuvent expliquer cet écart.
2. Si le nombre de décisions ne correspond pas à la page des requêtes : la page des décisions ne consigne que les événements de routage ; les requêtes qui n’y sont pas entrées (par exemple, bloquées en amont) n’y apparaissent pas.
3. Si « routable = 0 » mais que la requête a nevertheless abouti : le système a utilisé un repli entre fournisseurs ou un canal de secours ; vous pouvez voir la chaîne de repli dans les détails de la décision.
4. Écart temporel : la fin d’une période de refroidissement et la remise en service d’un identifiant évoluent à l’échelle des minutes ; si les deux consultations sont espacées de quelques minutes, les résultats peuvent différer.
