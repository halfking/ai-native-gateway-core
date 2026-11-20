# Page de détails sur l’état de santé des sondes

Dossier détaillé d’une cible de sonde : taux de réussite historique des sondes, répartition des latences et détail des échecs.

## Règles de calcul

Sonde = appel de test déclenché activement par le système ; le taux de réussite est calculé selon le nombre de sondes et reste distinct du trafic métier.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

- 1.Toutes les sondes réussissent, mais les opérations métier échouent : le chemin de la sonde peut différer du chemin réel (par exemple, requête simplifiée) ; le trafic réel fait foi.
- La latence ne correspond pas aux enregistrements des requêtes : les sondes utilisent une petite requête fixe ; leur latence est donc naturellement plus faible.
