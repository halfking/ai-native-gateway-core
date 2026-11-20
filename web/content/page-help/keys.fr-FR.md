# Explications des données de la page de liste des clés

Cette page gère les « clés » d’appel du gateway : chaque clé représente une identité pouvant lancer des appels à un modèle.

## Champs de chaque ligne

- Nom et préfixe : identifiant lisible de la clé ; la clé complète n’est affichée qu’une seule fois, au moment de son émission.
- Statut : voir les quatre statuts ci-dessous.
- Utilisation et quota : part déjà consommée par rapport au quota.

## Comment les quatre statuts sont déterminés

| Statut | Condition |
| --- | --- |
| En attente d’activation | La clé a été émise, mais sa première validation n’est pas encore terminée |
| Activé | Statut normal + clé non expirée + clé non désactivée ; les trois conditions doivent être réunies |
| Expirée | Une date d’expiration a été définie et cette date est passée |
| Révoquée | La clé a été désactivée manuellement |

Remarque : « Activé » exige que les trois conditions soient réunies — si la clé est expirée ou désactivée, la page affiche « Expirée » ou « Révoquée », même si le champ d’état en arrière-plan indique encore « Normal ».

## Méthode de calcul de l’utilisation

- Les chiffres d’utilisation comptabilisent les **appels terminés** ; les requêtes en cours ne sont pas prises en compte.
- Les données d’utilisation présentent un léger délai : un appel tout juste lancé peut mettre quelques instants à apparaître dans l’utilisation.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Vérifiez le type sélectionné dans les onglets de filtre par statut : les onglets en haut de la page permettent de filtrer la liste par statut, et son contenu change selon la sélection.
2. L’expiration est déterminée selon l’heure du serveur ; un décalage de l’horloge de votre ordinateur n’a aucune influence sur cette détermination.
3. Les clés intégrées au système (utilisées par la plateforme elle-même) apparaissent systématiquement dans la liste et ne sont pas incluses dans les statistiques standard.
4. Le quota et l’utilisation proviennent de deux sources de données avec des fréquences de mise à jour différentes : l’utilisation est actualisée en quasi temps réel et les modifications du quota prennent effet immédiatement. Si les chiffres ne concordent pas, actualisez d’abord la page.
