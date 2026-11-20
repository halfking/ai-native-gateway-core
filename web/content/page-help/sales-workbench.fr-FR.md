# Explication des données de la page de préparation des devis

Cette page est un espace de travail interne pour préparer les devis destinés aux clients : enregistrez d’abord « ce que nous pouvons vendre », puis « ce que le client veut », et le système génère le devis.

## Quatre onglets

- **Offres disponibles** : liste des modèles actuellement commercialisables sur la plateforme (avec prix de revient, devise et niveau de confiance, tous calculés côté serveur et non modifiables manuellement).
- **Demandes** : modèles et volumes d’utilisation souhaité par le client.
- **Devis** : devis définitif établi après rapprochement de l’offre et de la demande (doté d’un numéro de version, il peut être approuvé et partagé).
- **Tableau de bord** (superadministrateur uniquement) : vue d’ensemble du pipeline des devis et de la dérive des coûts.

## Règles de calcul clés

- **Base de coût** : le prix de revient de chaque ligne de devis provient du tableau des prix de revient de la page de gestion des tarifs ; celui indiqué dans le devis est un instantané figé au moment de sa génération. Les changements de prix de revient ultérieurs ne s’appliquent pas rétroactivement aux devis déjà émis.
- **Version** : chaque révision d’un même devis crée une nouvelle version ; les versions précédentes sont en lecture seule et permettent de assurer un audit ultérieur.
- **Indicateur de dérive des coûts** : un signalement apparaît lorsque l’écart entre le prix de revient utilisé pour le devis et le prix de revient actuel dépasse le seuil défini, afin d’éviter d’émettre un devis fondé sur des coûts obsolètes.

## Comment vérifier les divergences de chiffres

1. Si un modèle est introuvable dans les offres disponibles : vérifiez d’abord que son prix de revient a bien été enregistré dans la gestion des tarifs ; un modèle sans prix de revient ne peut pas être vendu.
2. Si le prix de revient du devis diffère du prix de revient actuel : le devis conserve celui du moment de sa génération ; consultez l’indicateur « Dérive des coûts » pour déterminer s’il faut refaire le devis.
3. Si une entrée disparaît de la rubrique Demandes : une demande déjà transformée en devis y est archivée et devient visible sur la page des devis.
