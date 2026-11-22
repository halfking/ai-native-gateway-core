# Règles de calcul du tableau de bord et des statistiques de session

Le « Tableau de bord » et les « Statistiques de session » résument le même flux d'appels de modèles selon **deux règles de calcul**. Cette page explique comment chaque nombre de chaque onglet est obtenu, d'où viennent les données et quelle fenêtre temporelle ils couvrent, avec un tableau de confrontation qui relie chaque écart à sa cause. Objectif : aligner les données et vérifier soi-même les écarts.

> Base de mesure : 2026-11-23 02:27 (Asia/Shanghai) en production, recoupé avec des requêtes en lecture seule sur la base de production.

## Comment les deux onglets s'articulent

| | Tableau de bord | Statistiques de session |
| --- | --- | --- |
| Entité statistique | Un appel de modèle compte pour une ligne (**niveau requête**) | Une session compte pour une ligne portant ses valeurs **cumulées** (**niveau session**) |
| Source de vérité | Table de faits au niveau requête `usage_facts` (base en cache Redis + delta temps réel) | Table de résumé des sessions `session_summaries` (requête directe) + vues matérialisées (couche en cache) |
| Fenêtre temporelle | Jours calendaires dans le fuseau du système : minuit de aujourd'hui-(N-1) à maintenant | Panneau = N jours glissants (première requête dans les N derniers jours) ; ligne d'alignement = journées calendaires complètes de Shanghai |
| Trafic de sonde | Exclu via la classe de trafic de la table de faits | Exclu via les règles d'« univers de sessions » côté sessions |

**Conclusion d'abord** : les chiffres des deux onglets **ne doivent pas coïncider** — entité différente (requête ou session), fenêtre différente, règles d'exclusion différentes. Toute comparaison doit d'abord aligner ces trois points ; on ne soustrait pas directement.

## Tableau de bord : origine de chaque chiffre

| Carte / module | Calcul | Remarques |
| --- | --- | --- |
| Requêtes totales | Nombre de lignes à état final dans la fenêtre | Uniquement les états finaux (succès, échec, limitation) ; les flux non terminés sont exclus |
| Taux de réussite | Lignes réussies ÷ lignes totales | Part des requêtes ayant répondu |
| Tokens totaux | Somme des tokens d'entrée et de sortie de chaque requête | Les tokens en cache suivent la convention de chaque fournisseur |
| Coût total | Somme du coût de chaque requête (dollars) | Les dollars sont le coût payé au fournisseur amont |
| Crédits totaux | Crédits facturés par la plateforme aux locataires | ★ voir « La carte des crédits » |
| Latence moyenne | Somme des latences ÷ lignes totales | Millisecondes |
| Modèles / fournisseurs actifs | Comptage distinct de ceux qui ont reçu du trafic | Pas le « total disponible sur la plateforme » |
| Clés actives | Nombre de clés actuellement activées | **Selon la configuration, indépendant de la fenêtre** |
| Nombre d'erreurs | Somme par catégorie de la répartition des échecs ; à défaut, estimé par « requêtes totales × (1 − taux de réussite) » | L'estimation peut différer de quelques lignes |
| Camemberts (modèle / fournisseur / locataire / client…) | Requêtes, tokens et coût regroupés par dimension | Issus de la table des statistiques à la minute, pas de la même source que les cartes ci-dessus |
| Courbe d'usage | Requêtes, tokens, coût et crédits par tranche de temps | Également issue de la table des statistiques à la minute |

### Comment la fenêtre temporelle est calculée

- « Aujourd'hui » = minuit aujourd'hui dans le fuseau du système (Asia/Shanghai par défaut) jusqu'à maintenant ; « 7 derniers jours » = minuit il y a 6 jours jusqu'à maintenant ; « 30 derniers jours » de même. **Le début est un minuit de jour calendaire, la fin est l'instant présent** — pas une journée entière.
- Une plage personnalisée agrège les deux jours choisis, dernier jour complet compris.
- La page se rafraîchit toutes les 10 secondes ; si le canal temps réel est actif, les requêtes nouvellement terminées sont ajoutées aussitôt.

### La carte des crédits (règle particulière, à connaître)

La carte des crédits **ne montre pas** les crédits consommés dans la fenêtre choisie : la colonne des crédits de la table de faits au niveau requête vaut toujours 0 (mesuré : les 298 037 lignes valent 0). La valeur affichée est le **delta temps réel postérieur à la base en cache**. Donc :

- Passer de « Aujourd'hui / 7 derniers jours / 30 derniers jours » laisse la carte des crédits **inchangée** (mesuré : 86 925 dans les trois cas).
- La courbe d'usage de la même page porte une autre somme de crédits (table à la minute : 1 767 183 sur 7 jours), qui ne correspond pas à la carte.

Pour un registre de crédits fiable, utilisez les pages « Coûts d'usage » et « Rapprochement » ; la carte des crédits du tableau de bord ne convient pas aux comparaisons de séries temporelles.

## Statistiques de session : origine de chaque chiffre

| Module | Calcul | Remarques |
| --- | --- | --- |
| Sessions totales | Sessions dont la première requête tombe dans la fenêtre | Comptage au niveau session, ce n'est pas un nombre de requêtes |
| Sessions actives / nouvelles / fermées | Selon que la dernière ou la première requête tombe dans les dernières 24 heures | Règle de 24 heures, indépendante de la fenêtre principale |
| Coût total (USD) | Somme des coûts cumulés par session | **Tout** le coût d'une session est porté par sa ligne |
| Requêtes totales | Somme du nombre de requêtes cumulé par session | Également la valeur cumulée de session, pas les requêtes survenues dans la fenêtre |
| Coût d'entrée / de sortie | Somme des coûts d'entrée et de sortie de chaque session | Les deux font le coût total |
| Coût moyen par session / par requête | Coût total ÷ sessions (÷ requêtes) | Le dénominateur est la valeur cumulée de session |
| Taux de réussite / de cache (ligne d'alignement) | Lignes réussies ÷ total de requêtes de la vue au niveau requête | Ces deux taux n'existent qu'au niveau requête |
| Ligne d'alignement de l'analyse de session | Agrégat de session issu des vues matérialisées | Rafraîchi toutes les 15 minutes ; fenêtre = journées calendaires complètes de Shanghai |
| Top 5 utilisateurs / Top 5 fournisseurs | API de résumé de l'analyse de session | Le fournisseur est déduit du préfixe du nom de modèle et classé par nombre de requêtes ; **sans coût** |
| Rapprochement des tours | Nombre de requêtes de session contre nombre de tours | Au-delà de 10 % d'écart, la page affiche un avertissement |

### L'« univers de sessions » (quelles sessions sont comptées)

Une session n'est comptée que si les trois conditions sont réunies :

1. elle est enregistrée dans la table des sessions ;
2. elle possède un propriétaire (clé utilisateur ou clé de propriétaire de clé API ; si les deux sont vides, elle n'est pas comptée) ;
3. ce n'est pas une session de sonde : ni marquée manuellement comme sonde, ni reconnue par le motif « propriétaire / type de client / titre » comme auto-vérification, garde de déploiement, test ou vérification.

### Pourquoi le coût est ici plus faible que sur le tableau de bord

La troisième règle de l'univers de sessions écarte des **sessions métier réelles** classées comme sondes. Mesure en base de production (même fenêtre de 7 jours, 2026-11-23) :

- les 15 898 sessions écartées par le côté sessions ont leur requête principale classée à **100 % en trafic métier** dans la table de faits, pour 6 452,73 $ au total ;
- sur la même fenêtre, le trafic réellement classé comme sonde dans la table de faits ne totalise que **1,81 $**.

Autrement dit : les deux onglets prétendent exclure la sonde, mais **n'excluent pas les mêmes requêtes**. C'est la première raison pour laquelle le coût des statistiques de session est nettement inférieur à celui du tableau de bord.

## Confrontation des chiffres (mesuré le 2026-11-23, fenêtre de 7 jours)

| Indicateur | Tableau de bord | Statistiques de session (panneau) | Statistiques de session (ligne d'alignement) |
| --- | --- | --- | --- |
| Entité statistique | requêtes | sessions + requêtes cumulées de session | sessions / requêtes |
| Requêtes | 65 774 | 72 098 | — |
| Coût (USD) | 8 013,93 | 2 717,62 | 2 468,73 (coût interne) |
| Crédits | 86 925 (indépendant de la fenêtre) | — | 0 |
| Tokens | 3 245 804 009 | — | — |
| Taux de réussite | 95,31 % | — | 94,64 % |
| Sessions | — | 19 235 | 17 390 |

À quoi correspond chaque écart :

- **Requêtes 72 098 > 65 774** : le nombre de requêtes côté sessions est le cumul des requêtes des sessions retenues ; une session qui dépasse la limite de la fenêtre apporte aussi ses requêtes postérieures à la fenêtre. De plus, la table des sessions contient des lignes en double (mesuré : jusqu'à 30 lignes pour une même clé de session), elles aussi agrégées.
- **Coût 2 717,62 < 8 013,93** : l'univers de sessions retire les 6 452,73 $ de trafic métier décrits ci-dessus ; le tableau de bord compte en outre les appels **sans session rattachée** (appels directs à l'API, appels par script), mesurés à environ un cinquième du coût du tableau de bord.
- **Crédits 0 contre 86 925** : ni le côté sessions ni la table de faits au niveau requête ne détiennent actuellement de stock de crédits fiable ; la valeur affichée vient du delta temps réel.
- **Taux de réussite 95,31 % contre 94,64 %** : les deux au niveau requête, mais l'un vient de `usage_facts`, l'autre de la vue matérialisée du journal des requêtes, et les fenêtres diffèrent.

## Que vérifier quand les chiffres ne concordent pas

1. **Aligner d'abord la fenêtre** : tableau de bord = depuis un minuit calendaire ; panneau des statistiques de session = N jours glissants ; ligne d'alignement = journées calendaires complètes de Shanghai. Même avec le même préréglage, ce sont trois fenêtres différentes.
2. **Aligner ensuite l'entité** : comparer requêtes avec requêtes, coût avec coût ; ne jamais confronter un nombre de sessions à un nombre de requêtes.
3. **Chercher les marqueurs de dégradation** : quand une carte affiche « dégradé / peu fiable / non calculé », ces zéros ne signifient pas « aucun trafic ».
4. **Vérifier l'interrupteur de sonde** : « exclure la sonde / inclure la sonde » du tableau de bord ne concerne que la synthèse ; les camemberts et la courbe restent toujours sur la base métier.
5. **« Aujourd'hui » peut échouer** : sur les statistiques de session, le préréglage « Aujourd'hui » peut dépasser le budget de 30 secondes de la base sous charge ; la page affiche alors une erreur, pas un zéro.

## Limites connues (mesuré le 2026-11-23)

1. Le préréglage « Aujourd'hui » des statistiques de session atteint de façon reproductible le délai de 30 secondes sur la base de production (problème d'index dans la jointure de l'univers de sessions ; plus la fenêtre est petite, plus un plan dégradé est probable). « 7 derniers jours » et « 30 derniers jours » fonctionnent.
2. L'agrégation côté sessions additionne aussi les lignes en double de la table des sessions : son nombre de requêtes et son coût ressortent trop élevés.
3. La carte des crédits du tableau de bord ne varie pas avec la fenêtre temporelle (voir plus haut).
4. La courbe du tableau de bord (table à la minute) et les cartes (table de faits au niveau requête) diffèrent d'environ 13,8 % sur 30 jours (160 451 contre 140 969 requêtes).
5. L'exclusion de la sonde ne retire pas le même trafic de part et d'autre (voir plus haut).
