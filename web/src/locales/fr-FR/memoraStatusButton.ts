// French locale for MemoraStatusButton
export default {
  state: {
    disabled: 'Désactivé',
    paused: 'Déconnecté',
    ok: 'Connecté',
    error: 'Échec de connexion',
    loading: 'Vérification',
  },
  
  panel: {
    title: 'Connexion Memora',
    closeLabel: 'Fermer',
    
    fields: {
      serviceUrl: 'URL du service',
      recentLatency: 'Latence récente',
      error: 'Erreur',
      check: 'Vérification',
      writeQueue: 'File d\'attente d\'écriture',
      processedErrored: 'Traité / En erreur',
      consecutiveErrors: 'Erreurs consécutives',
      recentWriteError: 'Erreur d\'écriture récente',
    },
    
    actions: {
      processing: 'Traitement…',
      reconnect: 'Reconnecter',
      checkConnectivity: 'Vérifier la connectivité',
      disconnect: 'Déconnecter l\'écriture',
      refresh: 'Actualiser',
      sessionContext: 'Contexte de session',
    },
  },
  
  error: {
    connectionFailed: 'Échec de connexion',
    checkFailed: 'Échec de la vérification',
  },
}
