# Explication des données de la page de surveillance des identifiants

Cette page surveille la disponibilité des identifiants « en amont » (clés de compte utilisées pour accéder aux différents fournisseurs de modèles), afin de détecter les problèmes avant que les utilisateurs ne signalent une panne.

## Explication des onglets

- **Liste** : vue d’ensemble de la santé de chaque identifiant, avec une ligne par identifiant (état actuel, dernière erreur, motif de désactivation).
- **Carte de chaleur** : visualisation chronologique des erreurs de chaque identifiant ; plus la couleur est foncée, plus le nombre d’erreurs est élevé.
- **Journal de routage** : suite chronologique des événements au niveau des identifiants (activation/désactivation/basculage).
- **Tendance des erreurs** : courbe des erreurs par heure, permettant de distinguer les pics d’erreurs des erreurs persistantes.
- **Santé des sondes** (réservé aux superadministrateurs) : résultats des appels de test que le système déclenche proactively sur chaque identifiant.

## Interprétation des étiquettes d’état

- « Normal » = aucune défaillance récente enregistrée.
- « Désactivation manuelle » = identifiant mis hors service manuellement par un administrateur ; la sonde automatique ne le réactivera pas.
- « Gestion automatique » = identifiant mis automatiquement hors service par le système à la suite d’échecs consécutifs (par exemple, après plusieurs confirmations de panne) ; il peut être désactivé manuellement de force ou attendre sa reprise automatique.
- Le badge du motif de désactivation est prioritaire sur l’état de santé : lorsqu’un identifiant présente à la fois des erreurs et un motif de désactivation, la page affiche le motif de désactivation.

## Score de santé et nombre d’erreurs

- Le nombre d’erreurs compte les **appels échoués terminés**, cumulés sur la période sélectionnée.
- L’échec d’une même requête n’est compté que sur l’identifiant utilisé à ce moment-là ; une relance réussie ne supprime pas l’échec déjà enregistré.

## Vérifications à effectuer lorsque les chiffres ne correspondent pas

1. Périodes temporelles différentes : aligner la période du filtre de cette page avec celle du tableau de bord avant de comparer les chiffres.
2. Le trafic de « sonde » correspond aux appels de contrôle lancés par le système lui-même ; il est comptabilisé séparément du trafic utilisateur. La disponibilité d’un identifiant s’évalue en combinant les résultats des sondes et le trafic réel.
3. Un identifiant vient d’être rétabli : son nombre d’erreurs peut encore afficher brièvement une ancienne valeur. Les graphiques sont agrégés par heure ; le résultat complet de l’heure en cours ne sera disponible qu’à l’heure suivante.
4. Si la liste est vide, vérifier d’abord les filtres : une combinaison de filtres sur le type d’identifiant, le tenant ou l’état peut exclure tous les résultats.
