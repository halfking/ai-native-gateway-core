// fix_gateway_structure2.mjs
//1) settings.ts x7: move modeEnum/modeDescriptions out of compression into a top-level gateway section (matching zh-CN)
//2) priceCompare.ts x7: add suggestModelTitle/suggestModelPlaceholder/suggestModelHint
//3) sales.ts x7: add modelNotInSellable
import { readFileSync, writeFileSync } from 'node:fs';

const settings = {
 'en-US': {
 mSimple: 'simple - Simple mode', mExpert: 'expert - Expert mode',
 dSimple: 'Simple mode: the home page is a short access list; expert tools are folded under \u201cAdvanced\u201d.',
 dExpert: 'Expert mode: full navigation with all pages (current default).'
 },
 'ja-JP': {
 mSimple: 'simple - \u30b7\u30f3\u30d7\u30eb\u30e2\u30fc\u30c9', mExpert: 'expert - \u30a8\u30ad\u30b9\u30d1\u30fc\u30c8\u30e2\u30fc\u30c9',
 dSimple: '\u30b7\u30f3\u30d7\u30eb\u30e2\u30fc\u30c9\uff1a\u30db\u30fc\u30e0\u306f\u63a5\u7d9a\u30ea\u30b9\u30c8\u306e\u307f\u3001\u30a8\u30ad\u30b9\u30d1\u30fc\u30c8\u6a5f\u80fd\u306f\u300c\u8a73\u7d30\u300d\u30b0\u30eb\u30fc\u30d7\u306b\u307e\u3068\u3081\u3089\u308c\u3066\u3044\u307e\u3059',
 dExpert: '\u30a8\u30ad\u30b9\u30d1\u30fc\u30c8\u30e2\u30fc\u30c9\uff1a\u5b8c\u5168\u306a\u30ca\u30d3\u30b2\u30fc\u30b7\u30e7\u30f3\u3068\u5168\u30da\u30fc\u30b8\uff08\u73fe\u5728\u306e\u30c7\u30d5\u30a9\u30eb\u30c8\uff09'
 },
 'zh-TW': {
 mSimple: 'simple - \u7c21\u6613\u6a21\u5f0f', mExpert: 'expert - \u5c08\u5bb6\u6a21\u5f0f',
 dSimple: '\u7c21\u6613\u6a21\u5f0f\uff1a\u9996\u9801\u662f\u63a5\u5165\u6e05\u55ae\uff0c\u5c08\u5bb6\u529f\u80fd\u6536\u5728\u300c\u9032\u968e\u300d\u7d44',
 dExpert: '\u5c08\u5bb6\u6a21\u5f0f\uff1a\u5b8c\u6574\u5c0e\u822a\u8207\u5168\u90e8\u9801\u9762\uff08\u76ee\u524d\u9810\u8a2d\uff09'
 },
 'de-DE': {
 mSimple: 'simple - Einfacher Modus', mExpert: 'expert - Expertenmodus',
 dSimple: 'Einfacher Modus: Die Startseite ist eine verk\u00fcrzte Zugriffsliste; Expertenwerkzeuge sind unter \u201eErweitert\u201c eingeklappt.',
 dExpert: 'Expertenmodus: Vollst\u00e4ndige Navigation mit allen Seiten (aktuelle Voreinstellung).'
 },
 'fr-FR': {
 mSimple: 'simple - Mode simple', mExpert: 'expert - Mode expert',
 dSimple: 'Mode simple : la page d\u2019accueil est une liste d\u2019acc\u00e8s r\u00e9duite ; les outils experts sont regroup\u00e9s sous \u00ab Avanc\u00e9 \u00bb.',
 dExpert: 'Mode expert : navigation compl\u00e8te avec toutes les pages (r\u00e9glage actuel par d\u00e9faut).'
 },
 'es-ES': {
 mSimple: 'simple - Modo simple', mExpert: 'expert - Modo experto',
 dSimple: 'Modo simple: la p\u00e1gina de inicio es una lista de accesos reducida; las herramientas expertas est\u00e1n plegadas en \u00abAvanzado\u00bb.',
 dExpert: 'Modo experto: navegaci\u00f3n completa con todas las p\u00e1ginas (valor predeterminado actual).'
 },
 'ar-SA': {
 mSimple: 'simple - \u0627\u0644\u0648\u0636\u0639 \u0627\u0644\u0628\u0633\u064a\u0637', mExpert: 'expert - \u0648\u0636\u0639 \u0627\u0644\u062e\u0628\u0631\u0627\u0621',
 dSimple: '\u0627\u0644\u0648\u0636\u0639 \u0627\u0644\u0628\u0633\u064a\u0637: \u0627\u0644\u0635\u0641\u062d\u0629 \u0627\u0644\u0631\u0626\u064a\u0633\u064a\u0629 \u0642\u0627\u0626\u0645\u0629 \u0648\u0635\u0648\u0644 \u0645\u062e\u062a\u0635\u0631\u0629\u060c \u0648\u0623\u062f\u0648\u0627\u062a \u0627\u0644\u062e\u0628\u0631\u0627\u0621 \u0645\u0637\u0648\u064a\u0629 \u0636\u0645\u0646 \u00ab\u0645\u062a\u0642\u062f\u0645\u00bb.',
 dExpert: '\u0648\u0636\u0639 \u0627\u0644\u062e\u0628\u0631\u0627\u0621: \u062a\u0646\u0642\u0644 \u0643\u0627\u0645\u0644 \u0645\u0639 \u062c\u0645\u064a\u0639 \u0627\u0644\u0635\u0641\u062d\u0627\u062a (\u0627\u0644\u0627\u0641\u062a\u0631\u0627\u0636\u064a \u0627\u0644\u062d\u0627\u0644\u064a).'
 }
};

//1) move modeEnum/modeDescriptions to top-level gateway section
for (const [loc, tr] of Object.entries(settings)) {
 const p = 'src/locales/' + loc + '/settings.ts';
 const lines = readFileSync(p, 'utf8').split('\n');
 const start = lines.findIndex((l) => l.trim() === 'modeEnum: {');
 if (start >=0) {
 // find the closing of modeDescriptions block (the line '},' at the same depth right before '},\n docs')
 let end = -1;
 let seenDesc = false;
 for (let i = start; i < lines.length; i++) {
 const t = lines[i].trim();
 if (t === 'modeDescriptions: {') seenDesc = true;
 if (seenDesc && t === '},') { end = i; break; }
 }
 lines.splice(start, end - start +1);
 } else {
 console.log(loc + ': no misplaced block, skip move');
 }
 const docsI = lines.findIndex((l) => l.trim() === 'docs: {');
 const block = [
 ' gateway: {',
 ' modeEnum: {',
 ' simple: \'' + tr.mSimple + '\',',
 ' expert: \'' + tr.mExpert + '\',',
 ' },',
 ' modeDescriptions: {',
 ' simple: \'' + tr.dSimple + '\',',
 ' expert: \'' + tr.dExpert + '\',',
 ' },',
 ' },'
 ];
 lines.splice(docsI,0, ...block);
 writeFileSync(p, lines.join('\n'));
 console.log(loc + ': gateway section placed top-level');
}

//2) priceCompare suggestModel* x7 (en-US gets it too per parity: en-US missing priceCompare? no - en-US has it; only6 locales)
const pc = {
 'zh-TW': { title: '\u9078\u64c7\u6a21\u578b', ph: '\u9078\u64c7\u8981\u5b9a\u50f9\u7684\u6a21\u578b\uff08\u53ef\u591a\u9078\uff09', hint: '\u4e0d\u9078\u64c7\u4efb\u4f55\u6a21\u578b\u6642\uff0c\u5c0d\u6240\u6709\u6a21\u578b\u5b9a\u50f9\uff1b\u9078\u64c7\u5f8c\u53ea\u5c0d\u9078\u4e2d\u7684\u6a21\u578b\u5b9a\u50f9\u3002' },
 'en-US': null, // en-US already has them
 'ja-JP': { title: '\u30e2\u30c7\u30eb\u3092\u9078\u629e', ph: '\u4fa1\u683c\u4ed8\u3051\u3059\u308b\u30e2\u30c7\u30eb\u3092\u9078\u629e\uff08\u8907\u6570\u9078\u629e\u53ef\uff09', hint: '\u30e2\u30c7\u30eb\u3092\u9078\u629e\u3057\u306a\u3044\u5834\u5408\u306f\u3059\u3079\u3066\u306e\u30e2\u30c7\u30eb\u304c\u5bfe\u8c61\u3001\u9078\u629e\u6642\u306f\u9078\u629e\u3055\u308c\u305f\u30e2\u30c7\u30eb\u306e\u307f\u3002' },
 'de-DE': { title: 'Modell ausw\u00e4hlen', ph: 'Zu bewertende Modelle ausw\u00e4hlen (Mehrfachauswahl m\u00f6glich)', hint: 'Ohne Auswahl werden alle Modelle bewertet; mit Auswahl nur die gew\u00e4hlten.' },
 'fr-FR': { title: 'Choisir un mod\u00e8le', ph: 'Choisir les mod\u00e8les \u00e0 tarifer (s\u00e9lection multiple)', hint: 'Sans s\u00e9lection, tous les mod\u00e8les sont tarif\u00e9s ; avec s\u00e9lection, seuls les mod\u00e8les choisis.' },
 'es-ES': { title: 'Elegir modelo', ph: 'Elegir los modelos a tarificar (selecci\u00f3n m\u00faltiple)', hint: 'Sin selecci\u00f3n se tarifican todos los modelos; con selecci\u00f3n, solo los elegidos.' },
 'ar-SA': { title: '\u0627\u062e\u062a\u064a\u0627\u0631 \u0627\u0644\u0646\u0645\u0648\u0630\u062c', ph: '\u0627\u062e\u062a\u0631 \u0627\u0644\u0646\u0645\u0627\u0630\u062c \u0644\u0644\u062a\u0633\u0639\u064a\u0631 (\u0627\u062e\u062a\u064a\u0627\u0631 \u0645\u062a\u0639\u062f\u062f)', hint: '\u062f\u0648\u0646 \u0627\u062e\u062a\u064a\u0627\u0631 \u064a\u062a\u0645 \u062a\u0633\u0639\u064a\u0631 \u062c\u0645\u064a\u0639 \u0627\u0644\u0646\u0645\u0627\u0630\u062c\u1d3b \u0648\u0628\u0627\u0644\u0627\u062e\u062a\u064a\u0627\u0631 \u0627\u0644\u0645\u062e\u062a\u0627\u0631\u0629 \u0641\u0642\u0637.' }
};
for (const [loc, v] of Object.entries(pc)) {
 if (!v) continue;
 const p = 'src/locales/' + loc + '/priceCompare.ts';
 let s = readFileSync(p, 'utf8');
 if (s.includes('suggestModelTitle')) { console.log(loc + ': priceCompare already has keys'); continue; }
 // insert before the last closing brace of the default export
 const idx = s.lastIndexOf('}');
 const block = ' suggestModelTitle: \'' + v.title + '\',\n suggestModelPlaceholder: \'' + v.ph + '\',\n suggestModelHint: \'' + v.hint + '\',\n';
 s = s.slice(0, idx) + block + s.slice(idx);
 writeFileSync(p, s);
 console.log(loc + ': priceCompare keys added');
}

//3) sales modelNotInSellable x7
const sales = {
 'en-US': 'This model is not in the sellable list',
 'zh-TW': '\u8a72\u6a21\u578b\u4e0d\u5728\u53ef\u552e\u6e05\u55ae\u4e2d',
 'ja-JP': '\u3053\u306e\u30e2\u30c7\u30eb\u306f\u8ca9\u58f2\u53ef\u80fd\u30ea\u30b9\u30c8\u306b\u3042\u308a\u307e\u305b\u3093',
 'de-DE': 'Dieses Modell ist nicht in der Verkaufsliste',
 'fr-FR': 'Ce mod\u00e8le ne figure pas dans la liste vendable',
 'es-ES': 'Este modelo no est\u00e1 en la lista de venta',
 'ar-SA': '\u0647\u0630\u0627 \u0627\u0644\u0646\u0645\u0648\u0630\u062c \u0644\u064a\u0633 \u0641\u064a \u0642\u0627\u0626\u0645\u0629 \u0627\u0644\u0645\u0628\u064a\u0639\u0627\u062a'
};
for (const [loc, v] of Object.entries(sales)) {
 const p = 'src/locales/' + loc + '/sales.ts';
 let s = readFileSync(p, 'utf8');
 if (s.includes('modelNotInSellable')) { console.log(loc + ': sales already has key'); continue; }
 const idx = s.lastIndexOf('}');
 s = s.slice(0, idx) + ' modelNotInSellable: \'' + v + '\',\n' + s.slice(idx);
 writeFileSync(p, s);
 console.log(loc + ': sales key added');
}
