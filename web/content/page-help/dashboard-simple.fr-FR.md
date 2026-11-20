# Explication des données de la page de vue d’ensemble en mode simple

Cette page est un tableau de bord « en un coup d’œil » : état général + consommation + actions rapides ; elle est conçue pour les utilisateurs qui ne consultent pas le tableau de bord en permanence.

## La page comporte trois parties

- **Cartes de statistiques** : volume de requêtes du jour, taux de réussite, coûts, etc.
- **État du système** : indique directement, à l’aide de couleurs et d’indications textuelles, si les composants clés fonctionnent normalement.
- **Tendance sur 7 jours** : un graphique en barres quotidien montre l’évolution du volume de requêtes.

## Périmètres de calcul

- Les cartes de statistiques utilisent le **périmètre du jour** (depuis minuit aujourd’hui jusqu’à maintenant, avec une journée délimitée en UTC).
- Le graphique de tendance et le tableau de bord en mode expert utilisent la **même source de données** : les chiffres affichés dans les deux modes ne se contredisent pas.
- Le graphique de tendance n’affiche que les « jours avec des données » : le jour 1 d’un nouveau déploiement, 1 seule barre est normale, et aucun zéro sans données n’est tracé.

## Comment vérifier les écarts lorsque les chiffres ne correspondent pas

1. Les « Requêtes du jour » sont décomptées avec une bascule à minuit UTC : avant 8 heures, heure de Pékin, le « jour actuel » du graphique correspond encore au dernier point de la veille.
2. Le dénominateur du taux de réussite est l’ensemble des requêtes (y compris celles qui ont été bloquées) : si le résultat ne correspond pas au « nombre de requêtes réussies ÷ le total auquel vous vous attendiez », vérifiez d’abord s’il existe des requêtes bloquées.
3. Les dépenses ne sont affichées que selon un seul périmètre : le coût interne (en dollars) et les crédits externes ne sont pas regroupés ; vérifiez-les séparément.
4. Si l’état d’un composant est indiqué comme « Normal » mais qu’une page ne s’ouvre pas : le contrôle d’état vérifie que les composants sont toujours actifs, et non que chaque fonctionnalité est disponible ; pour un problème précis, consultez la description de la page concernée.
