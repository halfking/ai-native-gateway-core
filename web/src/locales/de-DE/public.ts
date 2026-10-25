// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): this file was
// still entirely English. It is now written in German, and the decorative
// "·" separator, the em dashes standing in for punctuation and the
// triple-adjective lists are gone.
export default {
  download: {
    activateLink: 'Aktivierungsassistent',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'Download',
    support: 'Unterstützen',
  },
  agreement: {
    title: 'Nutzungsvereinbarung und Hinweise',
    intro: 'Willkommen beim AI-Nativen Kern-Gateway für Organisationen. Der Kern wird als Open Source geteilt, der Standard-Build wird AS IS für den Selbstbetrieb bereitgestellt. Lesen Sie das, bevor Sie fortfahren:',
    points: [
      'Rechte und Pflichten: Für Installation, Konfiguration, Sicherung und regelkonforme Nutzung in Ihrer eigenen Umgebung sind Sie verantwortlich. Wir liefern Software und Dokumentation. Wir übernehmen keine Garantie für geschäftliche Ergebnisse, für die Qualität von Modellen oder für die Verfügbarkeit Dritter.',
      'Datenerhebung: Geschäftsinhalte werden nicht erhoben. Die optionale Telemetrie umfasst Version, Exemplar-ID, Lizenzstatus und zusammengefasste Messwerte. Sie erfasst nie Prompts, Gesprächstexte, API-Schlüssel oder Daten, die eine Person unmittelbar identifizieren.',
      'Open-Source-Teilung: Ein Build ohne Anpassung ist eine Community-Teilung ohne kommerzielles SLA. Für individuelle Vorhaben wenden Sie sich über den Support an uns.',
      'Haftungsausschluss: Die Software wird AS IS bereitgestellt. Wir haften nicht für indirekte Verluste, Ausfallzeiten, Datenverlust oder die Folgen von KI-Entscheidungen. Schätzen Sie das Risiko selbst ab und treffen Sie entsprechende Schutzmaßnahmen.',
    ],
    disclaimer: 'Die vollständigen Bedingungen stehen in der vollständigen Vereinbarung. Das Anhaken bedeutet, dass Sie den oben genannten Umfang gelesen und verstanden haben.',
    checkbox: 'Ich habe gelesen und stimme zu',
    fullLink: 'Nutzungsvereinbarung und Datenverarbeitungsfreigabe',
    accept: 'Annehmen und fortfahren',
  },
  opAgreement: {
    downloadTitle: 'Bitte {title} vor dem Download lesen',
    activateTitle: 'Bitte {title} vor der Aktivierung lesen',
  },
}
