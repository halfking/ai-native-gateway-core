// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): removed the
// decorative "·" separator, the em dashes standing in for punctuation, and the
// triple-adjective lists ("auditable, customizable, self-hostable"). Rules
// live in DESIGN.md §7 "Voice" at the repo root.
export default {
  download: {
    activateLink: 'Activation wizard',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'Download',
    support: 'Support',
  },
  agreement: {
    title: 'User agreement & notice',
    intro: 'Welcome to the AI-Native Organization Core Gateway. The core is shared as open source, and the standard build is provided AS IS for self-hosting. Read this before you continue:',
    points: [
      'Rights and duties: you are responsible for installing, configuring, backing up and using the gateway in compliance inside your own environment. We ship the software and the documentation. We do not warrant business outcomes, model quality or the uptime of third parties.',
      'Data collection: business content is not collected by default. The optional telemetry covers the version, the instance ID, the license state and aggregated metrics. It never covers prompts, session text, API keys or personal data that identifies someone directly.',
      'Open-source sharing: a non-custom build is a community share without a commercial SLA. Contact us through Support for a bespoke engagement.',
      'Disclaimer: the software is provided AS IS. We are not liable for indirect loss, downtime, data loss or the consequences of AI decisions. Assess the risk and protect yourself accordingly.',
    ],
    disclaimer: 'See the full agreement for complete terms. Checking the box means you understand the scope above.',
    checkbox: 'I have read and agree to the',
    fullLink: 'User Agreement & Data Processing Authorization',
    accept: 'Accept and continue',
  },
  opAgreement: {
    downloadTitle: 'Please read {title} before downloading',
    activateTitle: 'Please read {title} before activating',
  },
}
