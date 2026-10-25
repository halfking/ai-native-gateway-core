// Auto-synced from en-US (ar-SA)
export default {
  title: 'Routing Overrides',
  subtitle: 'ثبّت أو احظر نماذج محددة لكل زوج (task_type, profile). تسري التغييرات خلال دقيقة واحدة عند تحديث OverrideStore.',
  correlationsLink: 'Correlations dashboard',
  summary: {
    total: 'Total active',
    bans: 'Bans',
    pins: 'Pins',
    expiring: 'Expiring in 7d',
  },
  filter: {
    activeOnly: 'Active only',
    taskType: 'Task type',
    taskTypePlaceholder: 'e.g. code, reasoning',
    profile: 'Profile',
    all: '(all)',
    refresh: 'Refresh',
    loading: 'Loading…',
    newOverride: '+ New override',
    cancel: 'Cancel',
  },
  create: {
    title: 'New routing override',
    hintPin: 'تثبيت: يُفرض النموذج في مقدمة المرشحين بغض النظر عن الدرجة.',
    hintBan: 'حظر: يُستبعد النموذج من قائمة المرشحين بالكامل.',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'لا يوجد تجاوز يطابق التصفية. اضغط "+ تجاوز جديد" لإنشاء واحد.',
    deleteConfirm: 'هل تحذف التجاوز #{id} ({mode} {model} لـ {task})؟',
    deleteFailed: 'Delete failed: ',
  },
}
