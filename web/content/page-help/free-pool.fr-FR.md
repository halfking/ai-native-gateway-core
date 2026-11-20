# Explication des données de la page du pool de ressources gratuites

Cette page gère le « pool de modèles à coût zéro » : les modèles des niveaux gratuits officiels ou communautaires de chaque fournisseur sont réunis dans un pool unique pour servir de repli lors du routage.

## Trois sections

- **Modèles gratuits du pool** : liste des modèles gratuits actuellement disponibles (un modèle ne compte comme « dans le pool » que si des identifiants utilisables sont disponibles).
- **Catalogue de référence** : fournisseurs gratuits connus et modèles attendus — « intégré » indique que le pool contient des identifiants actifs pour ce fournisseur ; dans le cas contraire, il est seulement « connu ».
- **Gestion des identifiants** : enregistrement (stockage chiffré en base) et statut des identifiants dédiés au pool gratuit.

## Priorité de routage

- Les routes de modèles déjà enregistrées dans le catalogue officiel sont **toujours prioritaires** sur le pool gratuit ; le pool gratuit sert uniquement de repli « lorsque tous les canaux officiels sont indisponibles ».
- Les modèles du pool ne font l’objet d’aucun engagement de SLA : le niveau gratuit peut être limité en débit ou retiré à tout moment par le fournisseur en amont.

## Comment vérifier les écarts lorsque les chiffres ne concordent pas

1. Un modèle présent dans le catalogue de référence n’apparaît pas dans la « liste des modèles du pool » : le catalogue est une « liste attendue » ; l’intégration réelle dépend du statut des identifiants. Le modèle n’entre dans le pool qu’après l’ajout d’identifiants.
2. Les appels à un modèle du pool échouent encore : les niveaux gratuits sont fréquemment limités en débit ; les échecs sont comptabilisés dans le nombre d’erreurs d’identifiants et le modèle est automatiquement désactivé après plusieurs échecs consécutifs.
3. Un modèle « gratuit » génère toutefois des frais : il est possible que le routage de repli ait réellement utilisé un canal officiel (le pool gratuit n’a pas été sollicité) ; le trajet réellement emprunté, visible dans le détail de la requête, fait foi.
