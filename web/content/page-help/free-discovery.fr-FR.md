# Explication des données de la page de découverte des ressources gratuites

Cette page est le « détecteur de modèles gratuits » : elle parcourt chaque fournisseur de modèles gratuits selon des modèles de recherche afin de découvrir les nouveaux modèles disponibles.

## Trois sections

- **Gestion des modèles de recherche** : définit les modèles de recherche indiquant « où effectuer le balayage et selon quels critères considérer un modèle comme gratuit ».
- **Tâches de découverte** : enregistrements des tâches qui déclenchent un balayage (quand elles ont eu lieu et combien de modèles ont été trouvés).
- **Résultats de découverte** : liste des modèles candidats trouvés par les tâches, en attente d’un examen manuel qui déterminera s’ils seront ajoutés au pool.

## Processus et règles de comptage

- Déclencher une découverte → le système effectue les recherches selon les modèles définis → les résultats sont **examinés avant d’être ajoutés au pool** : modèle détecté ≠ modèle disponible ; l’ajout au pool de ressources gratuites nécessite une confirmation manuelle.
- Le « nombre de découvertes » correspond au nombre de modèles candidats ; les occurrences d’un même modèle sont regroupées et celui-ci n’est ajouté qu’une seule fois au pool.
- La suppression d’un modèle de recherche n’affecte pas les enregistrements des tâches et des résultats déjà produits.

## Comment vérifier soi-même les écarts lorsque les chiffres ne correspondent pas

1. La tâche indique 3 modèles trouvés, mais la page des résultats ne contient que 2 entrées : les candidats en double ont été regroupés et le nombre d’entrées après regroupement fait foi.
2. Un modèle découvert n’est plus disponible après avoir été ajouté au pool : les offres gratuites étant très variables, les identifiants utilisés pour l’ajout au pool peuvent déjà être limités par le service en amont ; consultez l’état des identifiants sur la page du pool de ressources gratuites.
3. La tâche reste « en cours » : une réponse lente du service en amont peut rallonger la détection ; si la tâche n’est pas terminée après une demi-heure, vérifiez la connectivité réseau et la configuration des modèles de recherche.
