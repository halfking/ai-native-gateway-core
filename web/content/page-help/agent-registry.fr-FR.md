# Page de registre des agents

Liste des agents automatisés (scripts/robots) enregistrés dans le back-office, avec leur état de santé et leur attribution.

## Règles de calcul

En bonne santé = présence récente et appels réussis ; hors ligne = aucun message depuis plus d’un cycle de signal de présence.

## Comment vérifier les écarts entre les chiffres

- 1.Le nombre d’agents est indépendant du nombre d’utilisateurs : les agents sont des comptes de service et ne consomment pas de siège utilisateur.
- Deux enregistrements pour le même agent : la recréation d’une instance entraîne un nouvel ID ; l’utilisation historique est attribuée par ID.
- État sain mais appel en échec : l’état de santé repose uniquement sur le signal de présence et l’état de connexion ; consultez les journaux des requêtes pour identifier la cause précise de l’échec.
