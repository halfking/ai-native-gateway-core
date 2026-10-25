// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): this file was
// still entirely English. It is now written in French, and the decorative
// "·" separator, the em dashes standing in for punctuation and the
// triple-adjective lists are gone.
export default {
  download: {
    activateLink: 'Assistant d\'activation',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'Télécharger',
    support: 'Nous soutenir',
  },
  agreement: {
    title: 'Accord utilisateur et mentions',
    intro: 'Bienvenue sur la passerelle centrale pour organisations IA natives. Le cœur est partagé en open source et la version standard est fournie EN L\'ÉTAT pour un déploiement sur site. Lisez ceci avant de continuer :',
    points: [
      'Droits et obligations : vous êtes responsable de l\'installation, de la configuration, des sauvegardes et d\'un usage conforme dans votre propre environnement. Nous fournissons le logiciel et la documentation. Nous ne garantissons ni les résultats métier, ni la qualité des modèles, ni la disponibilité des tiers.',
      'Collecte de données : les contenus métier ne sont pas collectés par défaut. La télémétrie facultative porte sur la version, l\'identifiant d\'instance, l\'état de la licence et des métriques agrégées. Elle ne couvre jamais les prompts, le texte des conversations, les clés API ni les données identifiant directement une personne.',
      'Partage open source : une version non personnalisée est un partage communautaire sans SLA commercial. Pour une commande sur mesure, contactez-nous via le support.',
      'Clause de non-responsabilité : le logiciel est fourni EN L\'ÉTAT. Nous ne sommes pas responsables des pertes indirectes, des interruptions de service, de la perte de données ni des conséquences des décisions de l\'IA. Évaluez le risque et mettez en place les protections qui s\'imposent.',
    ],
    disclaimer: 'Les conditions complètes figurent dans l\'accord intégral. Cocher la case signifie que vous avez lu et compris la portée ci-dessus.',
    checkbox: 'J\'ai lu et j\'accepte la',
    fullLink: 'Accord utilisateur et autorisation de traitement des données',
    accept: 'Accepter et continuer',
  },
  opAgreement: {
    downloadTitle: 'Lisez {title} avant de télécharger',
    activateTitle: 'Lisez {title} avant d\'activer',
  },
}
