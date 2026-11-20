# Explication des données de la page de configuration des plugins

Cette page permet de choisir l’implémentation utilisée pour chaque « emplacement de plugin » de la plateforme (par exemple, le composant intégré ou un service d’identité externe pour l’authentification).

## Mode d’utilisation

- Chaque emplacement de plugin est présenté sous forme de carte : l’implémentation actuellement active et une liste déroulante des choix disponibles.
- **Les modifications ne prendront effet qu’après le redémarrage du service** : la page enregistre la configuration, mais la bascule se produit au prochain démarrage — un enregistrement réussi ne signifie pas que la bascule a été effectuée.

## Source de configuration

- Le haut de la page affiche la « Source de configuration » : elle indique si la valeur actuelle provient de la base de données ou d’une variable d’environnement.
- Une valeur provenant d’une variable d’environnement reste prioritaire même si elle est modifiée dans la page — pour une modification permanente, changez l’environnement de déploiement.

## Comment vérifier les écarts de valeurs

1. Après une bascule, le comportement de connexion ne change pas : vérifiez d’abord que le service a redémarré avec la nouvelle configuration.
2. L’état affiché sur la page ne correspond pas à l’état réel : suivez la « Source de configuration » — les variables d’environnement sont prioritaires sur les valeurs enregistrées dans la page.
3. Aucun choix n’est disponible pour un emplacement de plugin : les implémentations candidates pour cet emplacement ne sont pas déployées ; terminez d’abord l’installation du composant correspondant.
