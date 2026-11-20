# Explication des données de la page d’analyse de corrélation

Cette page répond à la question « En quoi les échecs sont-ils liés ? » : elle croise les différentes caractéristiques des requêtes (modèle, longueur, fournisseur) avec les résultats d’échec afin d’identifier les combinaisons où les échecs se concentrent.

## Méthode de lecture

- Chaque case croisée indique le nombre de requêtes pour cette combinaison et le taux d’échec ; plus la couleur est foncée, plus les échecs sont concentrés.
- « Corrélé » ne signifie pas « causal » : la page présente une association statistique ; le fait que A et B apparaissent ensemble ne signifie pas nécessairement que A provoque B.

## Périmètre de calcul

- Taux d’échec = nombre d’échecs dans la combinaison ÷ nombre total d’enregistrements dans cette combinaison ; les combinaisons dont l’échantillon est trop faible (inférieur au seuil) sont signalées afin d’éviter toute interprétation trompeuse.
- La fenêtre temporelle et les filtres déterminent le périmètre des statistiques ; avant de comparer deux séries de chiffres, vérifiez que la fenêtre est identique.

## Comment vérifier soi-même en cas d’écart entre les chiffres

1. Une case affiche un taux d’échec de 100 %, mais seulement 2 requêtes : il s’agit d’une fluctuation liée à un petit échantillon ; ne tenez pas compte de la significativité, conformément à l’indication de la page.
2. Le nombre d’erreurs diffère de celui de la page de surveillance des identifiants : cette page regroupe les données par « combinaison de caractéristiques de requête », tandis que la page des identifiants les regroupe par « identifiant » ; la dimension de regroupement est différente.
3. Une case devient vide après application d’un filtre : la combinaison ne dispose d’aucun échantillon pour ce filtre ; il s’agit de « données indisponibles », et non de « zéro échec ».
