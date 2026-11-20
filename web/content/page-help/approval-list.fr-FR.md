# Explication des données de la page de la liste des approbations

Cette page centralise les éléments nécessitant une décision manuelle : certaines opérations sensibles ou demandes dépassant le budget sont d’abord suspendues, en attente de validation par un administrateur.

## Flux des statuts

En attente d’approbation → (Approuvée | Rejetée) → Archivée.

- Les éléments « En attente d’approbation » conservent leur effet de suspension tant qu’ils ne sont pas traités (par exemple, une demande reste en attente pendant la période d’approbation).
- Les approbations sont soumises à une règle de délai : celles qui ne sont pas traitées avant l’expiration du délai sont automatiquement approuvées ou rejetées selon la configuration. La page de détail indique la règle applicable à chaque élément.

## Relation entre la page de configuration et la page de liste

- La page « Configuration des approbations » définit **quelles situations** nécessitent une approbation, qui doit l’accorder et le délai avant expiration.
- La page « Liste des approbations » recense les tâches en attente **réellement générées** conformément à la configuration.
- Modifier la configuration n’affecte que les nouvelles approbations générées par la suite, et non les approbations déjà présentes dans la liste.

## Comment vérifier en cas d’écart entre les chiffres

1. Le nombre d’éléments dans la liste est différent du nombre de notifications : une notification peut concerner un élément déjà traité par un tiers ; la page « En attente d’approbation » de la liste fait foi.
2. Le même élément fait l’objet de deux approbations : deux règles différentes ont chacune déclenché une approbation ; la page de détail indique la source du déclenchement.
3. L’approbation a été accordée, mais la fonctionnalité n’est pas active : la prise d’effet peut être légèrement différée ; si ce n’est toujours pas le cas après plus d’une minute, consultez la page de la fonctionnalité correspondante.
