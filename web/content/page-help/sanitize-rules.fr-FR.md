# Explication des données de la page des règles de masquage

Cette page gère la bibliothèque de règles de l’« intercepteur d’informations sensibles » : les motifs à remplacer/masquer dans le contenu envoyé au modèle (numéros de téléphone, numéros de pièce d’identité, chaînes de clés, etc.).

## Deux onglets

- **Règles** : liste des règles (chacune définit la manière de reconnaître et de remplacer un motif sensible).
- **Détections et validations** : enregistrements des blocages effectifs — la règle déclenchée à chaque emplacement d’une requête et le contenu de remplacement.

## Logique d’application des règles

- Chaque règle peut être activée ou désactivée indépendamment selon le « mode d’exécution » ; l’« Activation » globale est l’interrupteur général : lorsqu’il est désactivé, aucune règle ne s’applique.
- Les modifications des règles s’**appliquent immédiatement** aux requêtes ultérieures ; les requêtes historiques déjà envoyées ne sont pas retraitées.

## Périmètre des enregistrements de détection

- Une requête peut déclencher plusieurs règles, chaque détection étant enregistrée séparément.
- La « validation » est une étape de confirmation manuelle : confirmer que le remplacement est justifié ou identifier une fausse alerte, afin d’ajuster les règles en continu, sans modifier les requêtes déjà traitées.

## Comment vérifier les écarts

1. Des informations sensibles sont manifestement présentes, mais elles n’ont pas été bloquées : vérifiez d’abord l’interrupteur général et l’activation de la règle pour le mode concerné, puis confirmez que le motif défini par la règle correspond au contenu réel (certaines règles ne reconnaissent que des formats précis).
2. Le nombre de détections est supérieur à celui attendu : plusieurs correspondances dans un même contenu sont comptées séparément ; consultez le nombre de requêtes dédupliquées au niveau « Requête ».
3. Les fausses alertes sont nombreuses : dans « Détections et validations », regroupez les détections par règle pour voir laquelle concentre le plus de fausses alertes, puis ajustez les conditions de correspondance de cette règle au lieu de désactiver l’interrupteur général.
