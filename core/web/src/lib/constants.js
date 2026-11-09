import { t } from './i18n.js';

// Display text here stays Persian: it is looked up in the English dictionary
// (i18n/en/tunnels.js) where it is shown, so a language switch re-renders it.
export const TRANSPORTS = {
  tls: { name: 'TLS', help: 'TLS با اثرانگشت مرورگر؛ پیش‌فرض و مخفی‌کار' },
  ws: { name: 'WebSocket', help: 'درون TLS؛ از CDN عبور می‌کند (نیاز به مسیر WS)' },
  tcp: { name: 'TCP', help: 'سبک‌ترین و سریع‌ترین، بدون پوشش' },
  stealth: { name: 'Stealth', help: 'TCP رمزشده بدون اثر TLS؛ روی سیم فقط بایت تصادفی' },
  kcp: { name: 'KCP+FEC', help: 'روی UDP با تصحیح خطا؛ برای مسیرهای پرافت' },
  quic: { name: 'QUIC', help: 'روی UDP، شبیه HTTP/3' },
  icmp: { name: 'ICMP', help: 'درون بسته‌های پینگ؛ جایی که فقط ping باز است (نیاز به root؛ سرور به ping پاسخ نمی‌دهد)' },
  dns: { name: 'DNS (UDP)', help: 'KCP روی UDP که بسته‌هایش شبیه کوئری/پاسخ DNS روی پورت ۵۳‌اند؛ جایی که فقط DNS باز است' },
  grpc: { name: 'gRPC', help: 'یک تماس gRPC روی HTTP/2 و TLS؛ از CDNهای پشتیبان gRPC عبور می‌کند' },
  ssh: { name: 'SSH', help: 'از طریق sshd خودِ سرور مقابل؛ جایی که فقط SSH باز است (نیاز به یوزر/رمز SSH؛ پورت تازه‌ای باز نمی‌شود)' },
};

// the port a carrier listens on by default, per transport (ICMP has none)
export const DEF_PORT = { tls: 443, ws: 443, tcp: 8080, stealth: 8443, kcp: 4000, quic: 8443, grpc: 2096, dns: 53 };

export const trName = (k) => (TRANSPORTS[k] || {}).name || k;

// The listening side's address per transport: [config key, transport, label,
// default port, bind address]. ICMP has no port; SSH listens on loopback only.
export const LISTENERS = [
  ['listen', 'tls', 'TLS / WebSocket', 443, '0.0.0.0'],
  ['tcp_listen', 'tcp', 'TCP ساده', 8080, '0.0.0.0'],
  ['stealth_listen', 'stealth', 'Stealth (TCP رمزشده)', 8443, '0.0.0.0'],
  ['grpc_listen', 'grpc', 'gRPC (HTTP/2)', 2096, '0.0.0.0'],
  ['kcp_listen', 'kcp', 'KCP + FEC (UDP)', 4000, '0.0.0.0'],
  ['quic_listen', 'quic', 'QUIC (UDP)', 8443, '0.0.0.0'],
  ['dns_listen', 'dns', 'DNS (UDP) — شبیه DNS روی ۵۳', 53, '0.0.0.0'],
  ['icmp_listen', 'icmp', 'ICMP (پینگ؛ بدون پورت)', 0, '0.0.0.0'],
  ['ssh_listen', 'ssh', 'SSH (از طریق sshd این سرور)', 8710, '127.0.0.1'],
];

// The multiplexer protocol a relay tunnel's carriers speak. Version 2 gives
// every user connection its own flow window, so a slow user no longer holds
// up the others on the same carrier; both ends must use the same version,
// which the panel sets on both.
export const MUX_VERSIONS = [[1, 'نسخهٔ ۱ (پیش‌فرض)'], [2, 'نسخهٔ ۲: کنترل جریان جدا برای هر کاربر']];
export const MUX_HELP = 'در نسخهٔ ۲ کاربر کند بقیهٔ کاربران همان حامل را کند نمی‌کند. پنل هر دو سر را هم‌نسخه می‌کند؛ هر دو سرور باید ۲.۶.۷ یا جدیدتر باشند.';

export const PRESETS =[['balance', 'متعادل'], ['turbo', 'توربو'], ['aggressive', 'تهاجمی'], ['throughput', 'حداکثر عبور']];
export const presetName = (v) => t((PRESETS.find((p) => p[0] === v) || PRESETS[0])[1]);

export const STATE_FA = { active: 'فعال', inactive: 'متوقف', failed: 'خطا', activating: 'در حال شروع', deactivating: 'در حال توقف', unknown: 'نامشخص' };
export const stateClass = (s) => (s === 'active' ? 'ok' : s === 'failed' ? 'bad' : '');
