# Explication des données de la page de supervision des opérations

Cette page est le « tableau de bord global » multi-sites de déploiement : l’état du service central, des régions, des nœuds de déploiement et de la santé du plan de données, sur un seul écran.

## Quatre sections

- **Instances centrales** : état d’exécution et version du service central.
- **Répartition régionale** : répartition et état des sites de déploiement, par zone géographique.
- **Nœuds de déploiement** : déploiements récents (heure, version, résultat).
- **Plan de données** : volume et santé des principales tables de données.

## Définition des statuts

- Le statut d’un nœud est transmis régulièrement par ce nœud ; « hors ligne » signifie qu’aucun message n’a été reçu pendant plus d’un cycle de transmission. Le nœud peut être arrêté, mais une coupure réseau est également possible.
- La version correspond à celle signalée par le nœud lui-même ; plusieurs versions dans une même région indiquent un état intermédiaire de déploiement progressif.

## Fréquence d’actualisation

- Les données de cette page sont actualisées selon un cycle fixe (elles ne sont pas actualisées en temps réel à la seconde près) ; un déploiement qui vient d’être effectué n’apparaîtra qu’à l’actualisation suivante.
- Dans « N éléments récents », N correspond au nombre maximal d’éléments affichés sur la page, et non à la totalité de l’historique ; pour l’historique complet, consultez la gestion des déploiements.

## Comment vérifier en cas d’écart entre les chiffres

1. Vérifiez d’abord le moment de la consultation : il existe un intervalle de sondage entre les données de cette page et l’état affiché localement sur le nœud.
2. Si le total régional diffère de la somme des nœuds pris individuellement : lorsqu’un nœud n’a pas transmis ses données, il est traité comme « inconnu » et n’est pas inclus dans le total régional.
3. Une divergence de version n’est pas forcément indicative d’une panne : pendant un déploiement progressif, la coexistence des anciennes et des nouvelles versions est attendue ; une fois le déploiement terminé, les versions doivent converger vers une seule version.
