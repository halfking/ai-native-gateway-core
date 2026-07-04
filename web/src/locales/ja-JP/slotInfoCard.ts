// Japanese locale for SlotInfoCard
export default {
  loading: 'スロット情報を読み込み中...',
  retry: '再試行',
  disabled: 'この資格情報には指紋スロットが有効になっていません（FpSlot 無効）',
  
  stats: {
    totalSlots: '総スロット数',
    activeSlots: 'アクティブ',
    totalInflight: '同時リクエスト',
    slotLimit: 'スロット上限',
  },
  
  layers: {
    layer1: 'レイヤー 1: 指紋スロット',
    layer2: 'レイヤー 2: 実行中の詳細',
  },
  
  inflight: {
    count: '{count} 同時',
    holder: '保持者:',
    ttl: 'TTL:',
    expired: '期限切れ',
  },
  
  empty: '現在アクティブなスロットはありません',
  
  error: {
    fallback: 'スロット情報の読み込みに失敗しました',
  },
  
  alert: {
    releaseNotImplemented: 'スロット解放機能はまだ実装されていません（Phase 8 TODO）',
  },
}
