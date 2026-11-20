# Page de réécriture du routage

Forçage manuel du routage : rediriger le trafic d’un modèle ou d’une clé vers une cible spécifiée.

## Explication des règles

La réécriture est prioritaire sur la sélection automatique des routes ; elle prend effet immédiatement et peut être annulée à tout moment.

## Comment vérifier en cas d’écart entre les chiffres

- 1.La réécriture n’est pas effective : vérifiez la portée de correspondance des conditions (modèle/clé/organisation) ; des conditions trop restrictives ne permettent de faire correspondre aucun trafic.
- Après l’annulation, le trafic continue d’être envoyé vers l’ancienne cible : la décision est mise en cache pendant quelques secondes, puis l’ancienne cible est de nouveau utilisée après un court délai.
