# Explication des données de la page de l’arbre des étapes de requête

Cette page décompose la « vie » d’une requête en dix étapes, disposées sur une chronologie, afin d’identifier immédiatement où elle est bloquée en cas de problème.

## Dix étapes (par ordre d’occurrence)

Authentification/limitation du débit → Masquage des données → Approbation → Détection d’injection → Routage → Compression → Fournisseur/poole de Token → Premier octet → Réinsertion des données masquées → Comptage/Réponse.

- La piste d’envoi (de gauche à droite) correspond au processus d’envoi de la requête ; la piste de réception (de droite à gauche) correspond au retour de la réponse.
- Une requête ne passe pas nécessairement par toutes les étapes : si aucune approbation n’est déclenchée, l’étape « Approbation » affiche « Non applicable ».

## Sept états possibles pour les étapes

Non atteinte / En cours / Terminée / Non applicable / Échec / Avertissement / Non mesurée.

- « Échec » = l’étape a interrompu la requête (par exemple, détection d’une injection ou refus d’approbation).
- « Avertissement » = la requête a été autorisée, mais des éléments suspects ont été enregistrés.
- « Non mesurée » = aucune donnée de durée n’est disponible pour cette étape ; cela ne signifie pas qu’il y a eu un échec.

## Durée des étapes

- La longueur de la barre horizontale de chaque étape représente sa durée ; le temps entre « Premier octet » et « Réponse » correspond à la génération de la réponse par le modèle et est généralement le plus long.
- La durée totale ≠ la simple somme des durées des étapes : certaines étapes se produisent en parallèle.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. La durée d’une étape « En cours » continue d’augmenter ; elle ne se stabilise qu’une fois l’étape terminée.
2. Si une requête est introuvable, vérifiez d’abord les filtres : la page n’affiche que les requêtes correspondant aux filtres actifs et à la fenêtre temporelle sélectionnée.
3. La présence de nombreuses étapes « Non mesurée » signifie généralement que la requête a suivi l’ancien format de journalisation (données antérieures). Il s’agit d’un format de données historiques, et non d’une panne du système.
4. Pour les requêtes en échec, la référence est l’« Étape en échec » : pour diagnostiquer le problème, commencez par la première étape à l’état « Échec », car les étapes suivantes ne sont généralement pas exécutées.
