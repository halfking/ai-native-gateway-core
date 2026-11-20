# Explication des données de la page de protection contre les injections de Prompt

Cette page configure la « protection contre les instructions non autorisées » : elle identifie les contenus d’attaque de type « ignorer les règles / incarner le système » glissés dans les saisies utilisateur.

## Trois onglets

- **Stratégie** : interrupteur général et mode de fonctionnement.
- **Moteur LLM** : configuration du moteur qui utilise un modèle pour effectuer une seconde validation.
- **Règles de détection** : liste des règles de détection des caractéristiques spécifiques des attaques.

## Deux modes de fonctionnement (différence clé)

- **Mode observation (journalisation uniquement)** : consigne uniquement les résultats de détection, **ne bloque** aucune requête — permet d’évaluer d’abord le taux de fausses alertes avant de resserrer les règles.
- **Mode forcé** : exécute les actions définies par la « matrice de sévérité » (autoriser / soumettre à approbation manuelle / bloquer directement) ; un délai de réponse à l’approbation réglé sur 0 signifie une attente sans limite.

## Conventions de comptage

- Le nombre de correspondances de règles est compté comme « détecté » ; en mode observation, les correspondances **ne produisent aucun** blocage et sont comptabilisées séparément du mode forcé.
- La matrice de sévérité détermine « comment une même correspondance est traitée selon le mode » — toute modification de la matrice doit être vérifiée dans les deux colonnes simultanément.

## Comment effectuer un contrôle en cas d’écart entre les chiffres

1. Une correspondance est détectée, mais la requête n’est pas bloquée : le mode observation est probablement actif (comportement attendu) ; avant de passer en mode forcé, vérifiez d’abord les fausses alertes de cette règle.
2. Le taux de fausses alertes est élevé : examinez les exemples de correspondance règle par règle ; une fausse alerte fréquente est un message métier normal contenant une formulation telle que « ignorez les instructions ci-dessus » ; ajustez la règle plutôt que de désactiver l’interrupteur général.
3. En mode forcé, toutes les requêtes sont envoyées pour approbation : les niveaux de sévérité élevée sont tous configurés sur « approbation » ; lorsque l’approbateur est indisponible, cela se manifeste par le fait que « tout reste bloqué » ; ajustez la matrice ou le délai de réponse à l’approbation.
