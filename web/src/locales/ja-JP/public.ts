// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): this file was
// still entirely English. It is now written as natural Japanese, and the
// decorative "·" separator, the em dashes standing in for punctuation and the
// triple-adjective lists are gone.
export default {
  download: {
    activateLink: '激活ウィザード',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'ダウンロード',
    support: '支援する',
  },
  agreement: {
    title: '利用規約と注意事項',
    intro: 'AI-Native 組織向けコアゲートウェイへようこそ。コアはオープンソースとして共有し、標準ビルドは現状提供（AS IS）で自社運用できます。続ける前に、次を読んでください。',
    points: [
      '権利と義務: 自社の環境でのインストール、設定、バックアップ、合規での利用はご自身の責任です。ソフトウェアと文書をご提供しますが、業務結果、モデルの品質、第三者サービスの稼働時間については保証しません。',
      'データ収集: 既定では業務内容を収集しません。任意のテレメトリは、バージョン、インスタンス ID、ライセンス状態、集約された指標のみを扱い、プロンプト、会話本文、API キー、個人を直接特定できる情報は含みません。',
      'オープンソースでの共有: カスタマイズなしのビルドは、商用 SLA のないコミュニティ共有です。個別案件はサポートからご連絡ください。',
      '免責: ソフトウェアは現状提供です。間接的な損失、停止時間、データ損失、AI 判断の結果については責任を負いません。リスクを見積もり、適切な保護をしてください。',
    ],
    disclaimer: '完全な条件は利用規約の全文にあります。チェックを入れると、上記の範囲を読み理解了ことになります。',
    checkbox: '私は次を読み、同意します：',
    fullLink: '「利用規約とデータ処理の授権」',
    accept: '同意して次へ',
  },
  opAgreement: {
    downloadTitle: 'ダウンロード前に {title} を読んでください',
    activateTitle: '激活する前に {title} を読んでください',
  },
}
