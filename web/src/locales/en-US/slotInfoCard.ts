// English locale for SlotInfoCard
export default {
  loading: 'Loading slot info...',
  retry: 'Retry',
  disabled: 'This credential does not have fingerprint slots enabled (FpSlot disabled)',
  
  stats: {
    totalSlots: 'Total Slots',
    activeSlots: 'Active',
    totalInflight: 'Concurrent Requests',
    slotLimit: 'Slot Limit',
  },
  
  layers: {
    layer1: 'Layer 1: Fingerprint Slots',
    layer2: 'Layer 2: Inflight Details',
  },
  
  inflight: {
    count: '{count} concurrent',
    holder: 'Holder:',
    ttl: 'TTL:',
    expired: 'Expired',
  },
  
  empty: 'No active slots currently',
  
  error: {
    fallback: 'Failed to load slot info',
  },
  
  alert: {
    releaseNotImplemented: 'Slot release feature not implemented yet (Phase 8 TODO)',
  },
}
