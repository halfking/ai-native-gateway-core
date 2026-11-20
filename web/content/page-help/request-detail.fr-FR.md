# Explication des données de la page de détail d’une requête

Cette page présente **le dossier complet d’une requête** : le全过程 de son entrée dans la passerelle jusqu’à la production de la réponse, ainsi que chaque chiffre associé.

## Trois blocs

- **Informations de base** : heure, clé, modèle, statut, durée totale.
- **Détail du processus** : durée et résultat de chaque étape (authentification, sélection du chemin de traitement, appel en amont, réinsertion des données après masquage, mesure de consommation).
- **Contenu et consommation** : résumé du contenu réellement envoyé/reçu, volume de texte en entrée/sortie et coût de cette requête.

## Définitions clés

- **Durée totale** = délai entre la réception de la requête et le retour de la réponse complète. Elle comprend le temps de traitement de la passerelle et le temps de génération en amont, qui figurent séparément dans le détail.
- **Coût** calculé selon la grille tarifaire en vigueur au moment de la requête ; une modification ultérieure des tarifs ne change pas le coût de cette requête historique.
- Le résumé du contenu peut être tronqué (pour préserver les performances) ; consultez « Voir le texte intégral » pour accéder au contenu complet.

## Que faire en cas d’écart entre les chiffres

1. Si le coût de cette requête ne correspond pas au tarif du modèle : vérifiez d’abord l’heure de la requête et celle du changement de tarif — le prix est figé au **moment de la requête**.
2. Si le volume de texte en entrée est supérieur à votre propre comptage : le comptage du système inclut l’habillage des messages (prompt système, tours de conversation précédents), pas uniquement le texte que vous avez saisi.
3. Si le statut est « Réussi » mais que l’utilisateur n’a rien reçu : du point de vue de la passerelle, une requête réussie signifie que le service en amont a entièrement renvoyé la réponse. Une coupure réseau du client ensuite relève donc d’un problème sur la fin de la chaîne de communication ; consultez la chronologie des actions de la requête pour confirmer l’étape de retour de la réponse.
4. Traces de tentative : une même requête peut tester plusieurs candidats en interne, mais seul le dernier essai est reflected dans le statut final. Le détail du processus permet de voir toute la chaîne des tentatives.
