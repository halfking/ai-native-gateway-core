# Explication des données de la page d’analyse intelligente des erreurs

Cette page utilise l’IA pour vous aider à analyser « pourquoi les requêtes échouent » : les échecs présents dans la file d’erreurs sont confiés un par un (ou par lots) à l’agent intelligent, qui produit une détermination des causes et des recommandations de correction.

## Les deux onglets

- **File d’erreurs** : liste des échecs à analyser (une requête en échec par ligne).
- **Résultats d’analyse** : liste des conclusions produites par l’IA (catégories de causes, éléments justificatifs, recommandations).

## Périmètre des six cartes en haut

- Total / À analyser / Analysées / Échecs : statistiques issues de la **file d’erreurs** (domaine de la file).
- Taux de faux positifs / Temps moyen : statistiques issues des **résultats d’analyse** (domaine des résultats).
- Les trois cartes et les trois autres n’ont pas la même portée statistique ; leur addition n’a aucun sens — la page les présente regroupées par domaine.

## Mise en application des recommandations de correction

- Les recommandations de l’IA (modifier les règles/ajuster les niveaux/modifier les critères de correspondance) doivent être **approuvées manuellement** pour prendre effet ; un seuil de confiance s’applique lors de l’approbation, et les recommandations à faible confiance sont bloquées et doivent faire l’objet d’un réexamen.
- Chaque mise en application conserve une « capture de l’ancienne valeur », ce qui permet de revenir en arrière en un clic.

## Comment vérifier les écarts entre les chiffres

1. Le nombre d’éléments dans la file diffère du nombre d’échecs sur la page des requêtes : la file ne contient que les types d’échec **inclus dans le périmètre d’analyse** ; les requêtes bloquées en amont (par exemple, en cas d’échec d’authentification) n’y figurent pas.
2. « Analysées », mais introuvables sur la page des résultats : l’enregistrement des résultats d’analyse présente un court délai ; rafraîchissez la page avant de vérifier.
3. La même erreur apparaît à plusieurs reprises : les erreurs suivant le même schéma sont regroupées pour l’analyse ; la file affiche un échantillon représentatif.
