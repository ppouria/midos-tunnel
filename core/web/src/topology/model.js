import { trName } from '../lib/constants.js';
import { t, i18n } from '../lib/i18n.js';
import { KERNEL_MODES, CABLE, cableType } from './engine.js';

export { DEF_PORT } from '../lib/constants.js';
// shown through t() where it is rendered
export { STATE_TEXT } from '../lib/fleet.js';

export const uid = (p) => p + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);

export function serialize(topo) {
  return {
    self_role: topo.self_role,
    nodes: topo.nodes.map((n) => ({
      id: n.id, kind: n.kind, label: n.label || '', ip: n.ip || '', x: Math.round(n.x), y: Math.round(n.y),
      ssh_user: n.ssh_user || undefined, ssh_port: n.ssh_port || undefined,
    })),
    links: topo.links.map((l) => ({
      id: l.id, a: l.a, b: l.b, kind: l.kind, transport: l.transport || undefined, port: l.port || undefined,
      ports: l.ports || undefined, udp_ports: l.udp_ports || undefined, reverse: l.reverse || undefined,
      preset: l.preset || undefined, carriers: l.carriers || undefined, mux_version: l.mux_version > 1 ? l.mux_version : undefined,
      mode: l.mode || undefined, tunnel: l.tunnel || undefined,
    })),
  };
}

export function cableLabel(l, rtt) {
  const tr = l.kind === 'direct'
    ? trName(l.transport) + (l.transport === 'icmp' ? '' : ' :' + (l.port || ''))
    : t((KERNEL_MODES.find((m) => m[0] === l.mode) || [null, l.mode])[1]);
  return t(CABLE[cableType(l)].name) + ' · ' + tr + (rtt != null ? ' · ' + Math.round(rtt) + 'ms' : '');
}

// ------------------------------------------------------------ benchmark
export const toMbps = (s) => {
  const m = /([\d.]+)\s*([KMG])bps/.exec(s || '');
  return m ? parseFloat(m[1]) * ({ K: 0.001, M: 1, G: 1000 })[m[2]] : 0;
};
// bench.sh renamed some keys between versions (Total Mem -> Total RAM, TCP CC -> TCP Congestion Ctrl)
export const benchGet = (r, ...keys) => ((r.info || []).find((x) => keys.includes(x.key)) || {}).value || '';

export function benchSummary(bench, n) {
  if (bench.running[n.id]) return '⏳ ' + t('بنچمارک…');
  const r = bench.results[n.id];
  if (!r || !r.ok) return '';
  const parts = [];
  const best = (r.speed || []).reduce((a, s) => (toMbps(s.download) > toMbps(a.download || '') ? s : a), {});
  if (best.download) parts.push('↓ ' + best.download);
  const avg = (r.io || []).find((x) => /average/i.test(x.key));
  if (avg) parts.push('I/O ' + avg.value);
  return parts.join(' · ');
}

// bench.sh names its speedtest nodes "City, CC"; Hong Kong and Taipei carry
// their own region name here rather than the code bench.sh gives them
const COUNTRY_FA = {
  US: 'آمریکا', CA: 'کانادا', MX: 'مکزیک', BR: 'برزیل', AR: 'آرژانتین', CL: 'شیلی',
  GB: 'انگلستان', UK: 'انگلستان', IE: 'ایرلند', FR: 'فرانسه', DE: 'آلمان', NL: 'هلند', BE: 'بلژیک', LU: 'لوکزامبورگ',
  CH: 'سوئیس', AT: 'اتریش', IT: 'ایتالیا', ES: 'اسپانیا', PT: 'پرتغال', PL: 'لهستان', CZ: 'چک', HU: 'مجارستان',
  RO: 'رومانی', BG: 'بلغارستان', GR: 'یونان', SE: 'سوئد', NO: 'نروژ', FI: 'فنلاند', DK: 'دانمارک', EE: 'استونی',
  LV: 'لتونی', LT: 'لیتوانی', UA: 'اوکراین', RU: 'روسیه', TR: 'ترکیه', IR: 'ایران', AE: 'امارات', SA: 'عربستان',
  QA: 'قطر', BH: 'بحرین', OM: 'عمان', KW: 'کویت', IQ: 'عراق', IL: 'اسرائیل', AM: 'ارمنستان', GE: 'گرجستان', AZ: 'آذربایجان',
  KZ: 'قزاقستان', IN: 'هند', PK: 'پاکستان', CN: 'چین', HK: 'هنگ‌کنگ', TW: 'تایوان', JP: 'ژاپن', KR: 'کره جنوبی',
  SG: 'سنگاپور', MY: 'مالزی', TH: 'تایلند', VN: 'ویتنام', ID: 'اندونزی', PH: 'فیلیپین', AU: 'استرالیا', NZ: 'نیوزیلند',
  ZA: 'آفریقای جنوبی', EG: 'مصر',
};
const COUNTRY_EN = {
  US: 'United States', CA: 'Canada', MX: 'Mexico', BR: 'Brazil', AR: 'Argentina', CL: 'Chile',
  GB: 'United Kingdom', UK: 'United Kingdom', IE: 'Ireland', FR: 'France', DE: 'Germany', NL: 'Netherlands', BE: 'Belgium', LU: 'Luxembourg',
  CH: 'Switzerland', AT: 'Austria', IT: 'Italy', ES: 'Spain', PT: 'Portugal', PL: 'Poland', CZ: 'Czechia', HU: 'Hungary',
  RO: 'Romania', BG: 'Bulgaria', GR: 'Greece', SE: 'Sweden', NO: 'Norway', FI: 'Finland', DK: 'Denmark', EE: 'Estonia',
  LV: 'Latvia', LT: 'Lithuania', UA: 'Ukraine', RU: 'Russia', TR: 'Turkey', IR: 'Iran', AE: 'UAE', SA: 'Saudi Arabia',
  QA: 'Qatar', BH: 'Bahrain', OM: 'Oman', KW: 'Kuwait', IQ: 'Iraq', IL: 'Israel', AM: 'Armenia', GE: 'Georgia', AZ: 'Azerbaijan',
  KZ: 'Kazakhstan', IN: 'India', PK: 'Pakistan', CN: 'China', HK: 'Hong Kong', TW: 'Taiwan', JP: 'Japan', KR: 'South Korea',
  SG: 'Singapore', MY: 'Malaysia', TH: 'Thailand', VN: 'Vietnam', ID: 'Indonesia', PH: 'Philippines', AU: 'Australia', NZ: 'New Zealand',
  ZA: 'South Africa', EG: 'Egypt',
};
const CITY_FA = {
  'Los Angeles': 'لس‌آنجلس', Dallas: 'دالاس', 'New York': 'نیویورک', Chicago: 'شیکاگو', Miami: 'میامی', Seattle: 'سیاتل',
  'San Jose': 'سن‌خوزه', Montreal: 'مونترال', Toronto: 'تورنتو', Paris: 'پاریس', Amsterdam: 'آمستردام', London: 'لندن',
  Frankfurt: 'فرانکفورت', Berlin: 'برلین', Madrid: 'مادرید', Milan: 'میلان', Stockholm: 'استکهلم', Helsinki: 'هلسینکی',
  Warsaw: 'ورشو', Moscow: 'مسکو', Istanbul: 'استانبول', Dubai: 'دبی', Tehran: 'تهران', Mumbai: 'بمبئی',
  Suzhou: 'سوجو', Ningbo: 'نینگبو', Shanghai: 'شانگهای', Beijing: 'پکن', Guangzhou: 'گوانگجو', Shenzhen: 'شنجن',
  Chengdu: 'چنگدو', Nanjing: 'نانجینگ', Hangzhou: 'هانگجو', 'Hong Kong': 'هنگ‌کنگ', Taipei: 'تایپه',
  Singapore: 'سنگاپور', Tokyo: 'توکیو', Osaka: 'اوساکا', Seoul: 'سئول', Sydney: 'سیدنی',
};
const REGION_OF = { 'Hong Kong': 'HK', Taipei: 'TW' };

// The speedtest table as rows a person reads: country, city, ping, download
// and upload, with each node's download as a share of the fastest one.
export function speedRows(r) {
  const list = (r && r.speed) || [];
  const max = Math.max(1, ...list.map((s) => toMbps(s.download)));
  const fa = i18n.lang === 'fa';
  const countries = fa ? COUNTRY_FA : COUNTRY_EN;
  return list.map((s) => {
    const [city, cc0] = s.node.split(/,\s*/);
    const nearest = !cc0 && /speedtest/i.test(s.node);
    const cc = REGION_OF[city] || (cc0 || '').toUpperCase();
    const ping = parseFloat(s.latency);
    return {
      key: s.node,
      cc: nearest ? '' : cc,
      country: nearest ? t('نزدیک‌ترین سرور') : (countries[cc] || cc || '—'),
      city: nearest ? 'Speedtest.net' : ((fa && CITY_FA[city]) || city),
      failed: !!s.failed,
      ping: Number.isFinite(ping) ? ping : null,
      down: toMbps(s.download),
      up: toMbps(s.upload),
      share: toMbps(s.download) / max,
    };
  });
}
