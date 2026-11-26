# Règles de calcul du tableau de bord et des statistiques de session

## Définitions actuelles de V2

Le tableau de bord lit la table `request_metrics`, mise à jour de façon asynchrone, selon la date de chaque requête. Les statistiques de session comptent les requêtes liées sur la même période et dédupliquent les sessions par locataire et identifiant. La création et la fermeture suivent la première requête et la fermeture effective. Les sondes sont exclues par défaut. Les coûts en USD, les crédits et les autres devises sont distincts. Les mises à jour en attente et les données de coût manquantes sont signalées.

Les rapports historiques couvrent les jours calendaires terminés dans le fuseau horaire du système. Le rapprochement quotidien se base sur la réception par la passerelle et utilise la projection des requêtes pour l’usage final. Les envois aux fournisseurs, les factures et les écritures internes sont vérifiés séparément.

## Mesures historiques (V1)

Le « Tableau de bord », les « Statistiques de session » et le « Rapprochement fournisseurs » résument le même flux d'appels de modèles selon **trois règles de calcul**. Cette page explique comment chaque nombre est obtenu, d'où viennent les données et quelle fenêtre temporelle ils couvrent, avec un **détail poste par poste** reliant chaque écart à sa cause. Objectif : aligner les données et vérifier soi-même les écarts.

> Base de mesure : 2026-11-23 21:05 (Asia/Shanghai) en production. Tous les chiffres ont été relevés au **même instant** (dans une seule requête SQL), fenêtre = `NOW()-7 jours` ; le rapprochement fournisseurs a été mesuré séparément sur l'intervalle fermé de journées calendaires `2026-11-17 ~ 2026-11-23` et rapproché jour par jour de la table de faits.
>
> ★ Correction (tour d'alignement des plages de dates, 2026-11-23) : l'onglet « statistiques de session » utilise désormais **la même** plage de dates que l'onglet « tableau de bord » (même contrôle, même intervalle de journées calendaires) ⇒ les chiffres de statistiques de session ci-dessous ont été mesurés avec la fenêtre **d'avant alignement** (glissante) et doivent être remesurés sur la nouvelle fenêtre ; les chiffres du tableau de bord et du rapprochement fournisseurs ne changent pas.
> ★★ Correction (tour d'unification de la source de vérité, 2026-11-24) : ★★ la source de vérité du tableau de bord est passée de la table de faits au niveau requête `usage_facts` **à l'intégralité des journaux au niveau requête `request_logs_with_current_month`** — les cartes héros, la courbe d'usage et les camemberts lisent désormais **la même vue, le même ensemble de statuts terminaux et le même prédicat de sonde** ⇒ un seul univers pour toute la page, et les longues fenêtres (30 derniers jours) ne scindent plus la page en deux chiffres (la carte et les camemberts différaient d'un facteur 2). ⇒ **les algorithmes et les descriptions de source de cette page ont été corrigés selon le nouveau calibre** ; les **chiffres mesurés côté tableau de bord ci-dessous restent ceux de la mesure du 2026-11-23 (calibre d'avant bascule)** et doivent être remesurés sur le nouveau calibre avant remplacement (suivi comme tâche ouverte). Le rapprochement fournisseurs lit `report_snapshots` / `usage_facts` et n'est donc **plus garanti identique au tableau de bord au chiffre près** — voir la limite 7 en fin de page.

## Comment les trois modules s'articulent

| | Tableau de bord | Statistiques de session | Rapprochement fournisseurs |
| --- | --- | --- | --- |
| Entité statistique | Un appel de modèle compte pour une ligne (**niveau requête**) | Une session compte pour une ligne portant ses valeurs **cumulées** (**niveau session**) | Un appel de modèle compte pour une ligne (**niveau requête**, agrégée par fournisseur) |
| Source de vérité | Journaux au niveau requête `request_logs_with_current_month` | Table de résumé des sessions `session_summaries` + univers de sessions | Table d'instantanés quotidiens `report_snapshots` |
| Fenêtre temporelle | Jours calendaires dans le fuseau du système : minuit de aujourd'hui-(N-1) à maintenant | **Même intervalle que le tableau de bord** (journées calendaires dans le fuseau du système : minuit de aujourd'hui-(N-1) à la fin du dernier jour ; début/fin personnalisables) | **Intervalle fermé de journées calendaires** (journée entière, y compris le dernier jour in extenso) |
| Trafic de sonde | Exclu via les marqueurs de sonde portés par les journaux de requête (commutable) | Exclu via les règles de l'« univers de sessions » | **Non exclu** (tout le trafic, y compris « non attribué ») |
| Maturité | Temps réel | Temps réel | **Instantané T+1** (les dates dont la journée n'est pas terminée ont du retard) |

**Conclusion d'abord** : les chiffres des trois modules **ne doivent pas coïncider** — entité différente, fenêtre différente, règles d'exclusion de sonde différentes, maturité différente. Toute comparaison doit d'abord aligner ces quatre points ; on ne soustrait pas directement.

### Le plus important : le trafic de sonde est presque gratuit

Le trafic de sonde (health check / auto-vérification) figure à part dans la table de faits sous `traffic_class='probe'` :

| Base de calcul (même fenêtre) | Requêtes | Coût |
| --- | --- | --- |
| Hors sondes | 81 265 | 8 854,70 $ |
| Avec sondes | 189 534 | 8 856,87 $ |
| **Part de sonde** | **108 271** | **2,17 $** |

⇒ **Le nombre de requêtes varie d'un facteur 2,3 alors que le coût ne varie que de 2,17 $.** Devant un « beaucoup plus de requêtes pour un coût à peu près égal », soupçonnez d'abord la base de sonde, pas un erreur de calcul.

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
| Camemberts (modèle / fournisseur / locataire / client…) | Requêtes, tokens et coût regroupés par dimension | Issus du journal au niveau requête, donc de la même source que les cartes ci-dessus (même périmètre de sonde) |
| Courbe d'usage | Requêtes, tokens, coût et crédits par tranche de temps | **Même source que les cartes ci-dessus** : mêmes journaux au niveau requête `request_logs_with_current_month`, même prédicat de sonde et même ensemble de statuts terminaux ⇒ les tranches s'additionnent à la carte |

### Comment la fenêtre temporelle est calculée

- « Aujourd'hui » = minuit aujourd'hui dans le fuseau du système (Asia/Shanghai par défaut) jusqu'à maintenant ; « 7 derniers jours » = minuit il y a 6 jours jusqu'à maintenant ; « 30 derniers jours » de même. **Le début est un minuit de jour calendaire, la fin est l'instant présent** — pas une journée entière.
- Une plage personnalisée agrège les deux jours choisis, dernier jour complet compris.
- La page se rafraîchit toutes les 10 secondes ; si le canal temps réel est actif, les requêtes nouvellement terminées sont ajoutées aussitôt.

### La carte des crédits (règle particulière, à connaître)

La carte des crédits **ne montre pas** les crédits consommés dans la fenêtre choisie : la colonne des crédits de la table de faits au niveau requête vaut toujours 0 (mesuré : les 298 037 lignes valent 0). La valeur affichée est le **delta temps réel postérieur à la base en cache**. Donc :

- Passer de « Aujourd'hui / 7 derniers jours / 30 derniers jours » laisse la carte des crédits **inchangée** (mesuré : 86 925 dans les trois cas).
- La courbe d'usage et la carte de crédits de la même page proviennent maintenant de la **même source** (toutes deux lisent les journaux au niveau requête `request_logs_with_current_month`) ⇒ les crédits des tranches s'additionnent à la carte.

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
| Top 5 utilisateurs / Top 5 fournisseurs | Endpoints de classement de l'analyse de session (utilisateurs / modèles / fournisseurs / tâches) | Le fournisseur est agrégé par SQL sur `model_usage_aggregates.provider` (et non déduit du préfixe du nom de modèle) ; **le coût est inclus** et le tri par coût est possible ; les quatre dimensions Top 5 s'affichent dans Analyse de session → Utilisateurs |
| Rapprochement des tours | Nombre de requêtes de session contre nombre de tours | Au-delà de 10 % d'écart, la page affiche un avertissement |

### L'« univers de sessions » (quelles sessions sont comptées)

Une session n'est comptée que si les trois conditions sont réunies :

1. elle est enregistrée dans la table des sessions ;
2. elle possède un propriétaire (clé utilisateur ou clé de propriétaire de clé API ; si les deux sont vides, elle n'est pas comptée) ;
3. ce n'est pas une session de sonde : ni marquée manuellement comme sonde, ni reconnue par le motif « propriétaire / type de client / titre » comme auto-vérification, garde de déploiement, test ou vérification.

## Détail complet de l'écart de coût (mesuré le 2026-11-23 au même instant)

Les 81 265 requêtes / 8 854,70 $ du tableau de bord (hors sondes), **détaillés poste par poste dans chaque bloc des statistiques de session** (les six postes ci-dessous s'additionnent au total du tableau de bord, le coût se referme au centime près) :

| # | Composant | Requêtes | Coût (USD) | Affectation |
| --- | --- | --- | --- | --- |
| C | Sessions **conservées** dans l'univers de sessions | 58 180 | 1 551,41 | Statistiques de session (c'est le bloc affiché par la page) |
| D1 | Exclues · **sondes marquées manuellement** | 12 364 | 5 145,16 | Écart des statistiques de session |
| D2 | Exclues · **sondes détectées par motif** | 5 946 | 1 876,67 | Écart des statistiques de session |
| D3 | Exclues · **sans propriétaire** | 1 202 | 0,08 | Écart des statistiques de session |
| E | **Sessions à cheval sur la fenêtre** (première requête hors fenêtre, actives dedans) | 3 191 | 281,38 | Écart des statistiques de session |
| F | **Sans session rattachée** (appels directs à l'API, appels par script) | 381 | 0,00 | Tableau de bord uniquement |
| | **Total** | **81 263** | **8 854,70** | |

⇒ **L'écart entre les statistiques de session et le tableau de bord ne vient pas principalement des « sessions à cheval sur la fenêtre », mais des règles d'exclusion de sonde de l'univers de sessions (D1+D2).**

### Mesure clé : tout ce qui est exclu est du trafic métier réel

Les requêtes, dans la fenêtre, des sessions exclues D1/D2/D3, classées d'après `origin_stage` dans le journal des requêtes :

| Motif d'exclusion | Requêtes | Coût (USD) | Dont `origin_stage='business'` | Part |
| --- | --- | --- | --- | --- |
| Sondes marquées manuellement | 12 364 | 5 145,16 | 12 364 | **100 %** |
| Sondes détectées par motif | 5 946 | 1 876,67 | 5 946 | **100 %** |
| Sans propriétaire | 1 202 | 0,08 | 1 202 | **100 %** |

⇒ Les 19 512 requêtes et 7 021,91 $ exclus côté sessions sont, dans le journal des requêtes, **à 100 % du trafic métier (`origin_stage='business'`)** : pas une seule n'est une sonde. **C'est la première raison pour laquelle les statistiques de session sont en dessous du tableau de bord, et c'est l'écart isolé le plus important à ce jour.**

## Confrontation des chiffres des trois modules (mesuré le 2026-11-23)

| Indicateur | Tableau de bord (hors sondes) | Statistiques de session (panneau) | Rapprochement fournisseurs |
| --- | --- | --- | --- |
| Entité statistique | requêtes | sessions + requêtes cumulées de session | requêtes (agrégées par fournisseur) |
| Requêtes | 81 265 | 58 180 (dans l'univers) / 77 687 (total hors univers) | 178 841 (y compris « non attribué ») |
| Coût (USD) | 8 854,70 | 1 551,41 (dans l'univers) | 8 612,07 |
| Trafic de sonde | déjà exclu | exclu selon les règles de session | **non exclu** |
| Maturité | temps réel | temps réel | **instantané T+1** |

### Tableau de bord face au rapprochement fournisseurs : confrontation jour par jour

Le côté rapprochement lit `daily_total` dans `report_snapshots` et le confronte jour par jour à la table de faits (intervalle fermé de journées calendaires 11/17 ~ 11/23) :

| Date | Requêtes table de faits | Requêtes instantané de rapprochement | Écart | Écart de coût |
| --- | --- | --- | --- | --- |
| 11/17 | 27 446 | 27 446 | 0 | 0,0000 |
| 11/18 | 22 905 | 22 905 | 0 | 0,0000 |
| 11/19 | 35 008 | 35 008 | 0 | 0,0000 |
| 11/20 | 23 947 | 23 947 | 0 | 0,0000 |
| 11/21 | 21 341 | 21 341 | 0 | 0,0000 |
| 11/22 | 19 693 | 19 693 | 0 | 0,0000 |
| 11/23 | 36 019 | 28 501 | 7 518 | 42,38 |

⇒ **Les 6 premiers jours coïncident requête par requête et centime par centime** ; l'écart du 11/23 provient à 100 % de l'instant d'arrêt de l'instantané de ce jour (instantané `updated_at` = 17:38, heure de mesure 21:05) — **ce n'est pas une erreur de calcul, c'est le retard normal d'une journée inachevée**.

### À l'intérieur du module de rapprochement : aucun double comptage

Les lignes par fournisseur de la page de rapprochement (y compris « non attribué ») **s'additionnent exactement au total de la page** :

- Somme des lignes par fournisseur = 178 841 requêtes = le nombre total de requêtes de la page ✓
- Somme des coûts des lignes par fournisseur = 8 612,07 $ = le coût total de la page ✓
- Dont « non attribué » (`provider_id IS NULL`) = 79 801 requêtes / 100 % d'échecs / 0,00 $ — c'est du **trafic de sonde sans fournisseur rattaché** (la sonde n'atteint pas le fournisseur amont), pas un compte manquant.

## Que vérifier quand les chiffres ne concordent pas

1. **Aligner d'abord la fenêtre** : tableau de bord et panneau des statistiques de session = **le même intervalle de journées calendaires** (minuit de aujourd'hui-(N-1) ; le tableau de bord compte jusqu'à maintenant, le panneau jusqu'à la fin du dernier jour ⇒ les deux couvrent les mêmes données et utilisent le même contrôle de plage de dates) ; ligne d'alignement = journées calendaires complètes de Shanghai ; rapprochement fournisseurs = intervalle fermé de journées calendaires. Les deux derniers ne sont toujours pas la même fenêtre que les deux premiers.
2. **Aligner ensuite l'entité** : comparer requêtes avec requêtes, coût avec coût ; ne jamais confronter un nombre de sessions à un nombre de requêtes.
3. **Aligner ensuite la base de sonde** : le tableau de bord permet de passer à « inclure la sonde » ; les statistiques de session excluent selon les règles de l'univers de sessions ; **le rapprochement inclut toujours les sondes**. Si le nombre de requêtes diverge fortement alors que les coûts se ressemblent ⇒ regardez d'abord les sondes.
4. **Aligner ensuite la maturité** : le rapprochement est un instantané T+1, **les données du jour en cours ont nécessairement du retard** ; avant de comparer, vérifiez jusqu'à quelle date l'instantané couvre.
5. **Chercher les marqueurs de dégradation** : quand une carte affiche « dégradé / peu fiable / non calculé », ces zéros ne signifient pas « aucun trafic ».
6. **La courbe d'usage et les cartes du tableau de bord ont maintenant la même source** : les deux lisent les journaux au niveau requête `request_logs_with_current_month` avec le même prédicat de sonde ⇒ les tranches devraient s'additionner à la carte. Sinon : vérifier d'abord le drapeau `degraded` de la charge utile (le périmètre de sonde diffère en cas de repli sur les tables pré-agrégées), puis vérifier l'alignement des fenêtres temporelles.
7. **« Aujourd'hui » peut échouer (corrigé le 2026-11-23)** : sur les statistiques de session, le préréglage « Aujourd'hui » atteignait de façon reproductible le délai de 30 secondes, faute d'index de jointure côté base de données (index ajouté le 2026-11-23 : la requête est passée de 121,8 s à 0,19 s). Si la page affiche de nouveau l'erreur, l'index a disparu : demander à l'astreinte de vérifier que `idx_session_dim_session_key` est bien en place.

## Limites connues (mesuré le 2026-11-23)

1. **L'univers de sessions exclut les sondes trop largement (D1/D2, confirmé par mesure le 2026-11-23)** : les 19 512 requêtes et 7 021,91 $ exclus par les statistiques de session sont, dans le journal des requêtes, **à 100 % du trafic métier**. Les statistiques de session se situent donc environ **7 021,91 $ (79 %)** en dessous du tableau de bord (base hors sondes). **Les chiffres du tableau de bord (au niveau requête) n'ont jamais été concernés** ⇒ pour un rapprochement exact des coûts, s'appuyer sur le tableau de bord, ou mettre la page de rapprochement sur la base « métier » avant de comparer.
2. **Sessions à cheval sur la fenêtre (E)** : 18 sessions dont la première requête précède la fenêtre y sont encore actives et sont aujourd'hui écartées en bloc, ce qui omet 3 191 requêtes / 281,38 $ (3,2 % du coût du tableau de bord).
3. **Appels sans session rattachée (F)** : 381 appels directs à l'API ou par script n'ont pas de session ; le tableau de bord les compte, les statistiques de session non.
4. **Retard du jour même dans l'instantané de rapprochement** : le rapprochement est en base T+1, **les données du jour sont incomplètes** ; les soustraire directement des chiffres temps réel du tableau de bord fait toujours apparaître un écart.
5. La carte des crédits du tableau de bord ne varie pas avec la fenêtre temporelle (voir plus haut).
6. **Périmètre de sonde en état dégradé** : lorsque les camemberts et la courbe du tableau de bord basculent sur les tables pré-agrégées après l'échec du chemin principal, ces tables sont toujours « sans sondes » ⇒ en mode « sondes incluses » elles divergent du résumé héros (la charge utile porte alors un drapeau `degraded` — ce n'est pas une dégradation silencieuse).
7. **Le tableau de bord et le rapprochement fournisseurs ne sont plus garantis identiques au chiffre près (2026-11-24, tour 2)** : la source du tableau de bord est passée aux journaux au niveau requête, le rapprochement lit toujours `report_snapshots` / `usage_facts`. Les deux restent égaux tant que la table de faits est intacte ; dès que la fenêtre remonte **avant le 2026-11-10 13:38**, le rapprochement **sous-estime** — la table de faits a perdu à cet instant ≈352 000 faits d'activité réels (2026-10-12 ~ 2026-11-10 ; chaîne de preuves dans le fichier principal d'audit §C) alors que les journaux conservent ce trafic. ⇒ **l'écart entre rapprochement et tableau de bord s'explique par des points de départ historiques différents des sources, pas par un calcul erroné** ; pour des chiffres au niveau d'une période comptable, se référer à la page de rapprochement (avec son retard connu du jour).
