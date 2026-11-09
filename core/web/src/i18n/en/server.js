// English for messages the server sends in Persian: health checks, fleet
// install steps and errors, update notes. Messages that carry a value (an IP,
// a count, an error) are matched by the patterns at the bottom.
export default {
  'ورود لازم است': 'Sign-in required',

  // health checks (panel_doctor.go)
  'یونیت قالب تانل': 'Tunnel unit template',
  'فایل midos-tunnel@.service پیدا نشد؛ تانل‌های مدیریت‌شده بدون آن اجرا نمی‌شوند.': 'midos-tunnel@.service is missing; managed tunnels cannot run without it.',
  'نصب است.': 'Installed.',
  'به خطا خورده است.': 'Failed.',
  'فعال است.': 'Active.',
  'متوقف است.': 'Stopped.',
  'فوروارد IPv4': 'IPv4 forwarding',
  'روشن است.': 'On.',
  'خاموش است؛ برای تانل‌های کرنلی لازم است.': 'Off; kernel tunnels need it.',
  'کنترل ازدحام BBR': 'BBR congestion control',
  'فضای دیسک': 'Disk space',
  'گواهی TLS': 'TLS certificate',
  "صدور خودکار Let's Encrypt فعال است.": "Automatic Let's Encrypt certificates are on.",
  'گواهی منقضی شده است.': 'The certificate has expired.',
  'دسترسی خروجی HTTPS': 'Outbound HTTPS',
  'برقرار است.': 'Working.',
  'اتصال خروجی روی ۴۴۳ برقرار نشد؛ آپدیت و صدور گواهی ممکن است کار نکند.': 'Outbound connections on port 443 failed; updates and certificates may not work.',
  'همگام‌سازی ساعت': 'Clock sync',
  'وضعیت ساعت خوانده نشد.': 'Could not read the clock status.',
  'ساعت با NTP همگام است.': 'The clock is in sync with NTP.',
  'ساعت با NTP همگام نیست.': 'The clock is not in sync with NTP.',

  // fleet: requests
  'کلید میزبان سرور عوض شده است (سرور دوباره نصب شده؟)؛ سرور را دوباره اضافه کنید': "The server's host key has changed (was it reinstalled?); add the server again",
  'یکی از سرورهای این کابل پیدا نشد': "One of this cable's servers was not found",
  'نوع سرور باید ایران یا خارج باشد': 'The server type must be Iran or Abroad',
  'IP سرور معتبر نیست': 'The server IP is not valid',
  'پورت یا کاربر SSH معتبر نیست': 'The SSH port or user is not valid',
  'نصب این سرور در حال انجام است': 'This server is already being installed',
  'سرور مادر نیازی به افزودن ندارد': 'The mother server does not need to be added',
  'تعداد سرورها زیاد است': 'Too many servers',
  'رمز یا کلید SSH سرور را وارد کنید': "Enter the server's SSH password or key",
  'حداقل یک پورت منتقل‌شونده وارد کنید': 'Enter at least one port to forward',
  'بیش از ۳۲ سرور خارج روی یک سرور ایران ممکن نیست': 'An Iran server can have at most 32 abroad servers',
  'تانل کرنلی روی بعضی سرورهای خارج ساخته نشد': 'The kernel tunnel was not built on some abroad servers',
  'این کابل پیدا نشد؛ صفحه را تازه کنید': 'This cable was not found; reload the page',
  'این کابل پیدا نشد': 'This cable was not found',
  'این سرور را نمی‌توان حذف کرد': 'This server cannot be removed',
  'اسکریپت نصب روی سرور مادر نیست': 'The install script is not on the mother server',
  'این کابل هنوز اعمال نشده؛ آن را در طراحی شبکه تنظیم و اعمال کنید': 'This cable is not applied yet; set it up and apply it in the network designer',
  'نوع کابل باید مستقیم، معکوس یا کرنلی باشد': 'The cable type must be direct, reverse or kernel',
  'پورت حامل یا تعداد حامل معتبر نیست': 'The carrier port or carrier count is not valid',
  'نسخهٔ مالتی‌پلکسر باید ۱ یا ۲ باشد': 'The multiplexer version must be 1 or 2',
  'این سرور پیدا نشد؛ صفحه را تازه کنید': 'This server was not found; reload the page',
  'این سرور هنوز نصب نشده است؛ آن را با IP تازه دوباره اضافه کنید': 'This server is not installed yet; add it again with the new IP',
  'نقش این سرور در طراحی شبکه «ایران» نیست و کابل دارد؛ آن را در طراحی شبکه درست کنید': "This server's role in the network designer is not Iran and it has cables; fix that in the designer",

  // fleet: install steps
  'اتصال SSH و بررسی دسترسی root': 'SSH connection and root check',
  'کاربر SSH باید root باشد': 'The SSH user must be root',
  'نصب با ری‌استارت پنل نیمه‌کاره ماند؛ دوباره نصب کنید': 'The install was cut short when the panel restarted; install again',
  'نصب کلید سرور مادر': "Installing the mother server's key",
  'رمز SSH ذخیره نمی‌شود؛ مدیریت با کلید انجام می‌شود': 'The SSH password is not stored; the server is managed with a key',
  'خواندن مشخصات سرور': "Reading the server's specs",
  'به‌روزرسانی سیستم‌عامل': 'Updating the OS',
  'به انتخاب شما رد شد': 'Skipped, as you chose',
  'نصب پیش‌نیازها': 'Installing prerequisites',
  'نصب midos-tunnel': 'Installing midos-tunnel',
  'آماده‌سازی تانل کرنلی': 'Preparing kernel tunnels',
  'پیش‌نیاز کرنلی کامل نصب نشد؛ تانل مستقیم کار می‌کند': 'Kernel prerequisites were not fully installed; direct tunnels still work',
  'بنچمارک سرور (bench.sh)': 'Server benchmark (bench.sh)',
  'در پس‌زمینه شروع شد؛ نتیجه روی کارت سرور می‌آید': 'Started in the background; the result appears on the server card',

  // step labels of tunnel builds
  'این سرور': 'This server',
  'ESP (رمزدار)': 'ESP (encrypted)',

  // update
  'به‌روزرسانی نصب شد. سرویس‌ها و پنل در حال ری‌استارت‌اند؛ چند لحظه بعد صفحه را تازه کنید.': 'The update is installed. The services and the panel are restarting; reload the page in a moment.',
  'به نسخه‌ی قبلی برگشت؛ سرویس‌ها در حال ری‌استارت‌اند.': 'Rolled back to the previous version; the services are restarting.',
};

export const patterns = [
  // health checks
  [/^تانل ([A-Za-z0-9_.-]+)$/, 'Tunnel $1'],
  [/^فعال است ولی (\d+) بار ری‌استارت شده؛ لاگ را ببینید\.$/, 'Active, but restarted $1 times; check the log.'],
  [/^فعال نیست \(اکنون: (.*)؛ BBR توان عبور را بهتر می‌کند\)\.$/, 'Not active (now: $1; BBR improves throughput).'],
  [/^فقط (\d+)% آزاد است\.$/, 'Only $1% free.'],
  [/^(\d+)% آزاد است\.$/, '$1% free.'],
  [/^تا (\d+) روز دیگر منقضی می‌شود\.$/, 'Expires in $1 days.'],
  [/^تا (\d+) روز دیگر معتبر است\.$/, 'Valid for $1 more days.'],

  // fleet
  [/^اتصال SSH به (\S+) ناموفق بود: ([\s\S]*)$/, 'SSH connection to $1 failed: $2'],
  [/^سرور «(.+)» هنوز آماده نیست؛ اول آن را اضافه و نصب کنید$/, 'Server “$1” is not ready yet; add and install it first'],
  [/^سرور (\S+) قبلاً با نام «(.+)» اضافه شده است$/, 'Server $1 is already added as “$2”'],
  [/^اجرای فرمان روی سرور ناموفق بود: ([\s\S]*)$/, 'Running a command on the server failed: $1'],
  [/^نوشتن authorized_keys ناموفق بود: ([\s\S]*)$/, 'Writing authorized_keys failed: $1'],
  [/^ورود با کلید سرور مادر ناموفق بود \(PubkeyAuthentication در sshd خاموش است؟\): ([\s\S]*)$/, "Logging in with the mother server's key failed (is PubkeyAuthentication off in sshd?): $1"],
  [/^به‌روزرسانی کامل نشد؛ ادامه می‌دهیم \(([\s\S]*)\)$/, 'The update did not finish; continuing ($1)'],
  [/^نصب بسته‌ها ناموفق بود: ([\s\S]*)$/, 'Installing packages failed: $1'],
  [/^نسخهٔ (\S+) از قبل نصب است$/, 'Version $1 is already installed'],
  [/^نسخهٔ (\S+) از سرور مادر کپی شد$/, 'Version $1 copied from the mother server'],
  [/^نسخهٔ منتشرشده برای (\S+) نصب شد$/, 'Installed the published release for $1'],
  [/^کپی باینری ناموفق بود: ([\s\S]*)$/, 'Copying the binary failed: $1'],
  [/^معماری (\S+) پشتیبانی نمی‌شود$/, 'The $1 architecture is not supported'],
  [/^دانلود باینری (\S+) ناموفق بود: ([\s\S]*)$/, 'Downloading the $1 binary failed: $2'],
  [/^سرور در دسترس نیست: ([\s\S]*)$/, 'Server unreachable: $1'],
  [/^ترنسپورت (".*") برای کابل پشتیبانی نمی‌شود$/, 'Transport $1 is not supported for cables'],
  [/^به‌روزرسانی باینری روی «(.+)» ناموفق بود$/, 'Updating the binary on “$1” failed'],
  [/^نصب تانل روی «(.+)» ناموفق بود$/, 'Installing the tunnel on “$1” failed'],
  [/^تست از «(.+)» اجرا نشد: ([\s\S]*)$/, 'The test from “$1” did not run: $2'],
  [/^IP سرور ایران «(.+)» معتبر نیست$/, 'The IP of Iran server “$1” is not valid'],
  [/^IP سرور «(.+)» معتبر نیست$/, 'The IP of server “$1” is not valid'],
  [/^ساخت تانل کرنلی روی «(.+)» ناموفق بود$/, 'Building the kernel tunnel on “$1” failed'],
  [/^کابل حذف شد ولی پاک‌سازی بعضی سرورها ناموفق بود: ([\s\S]*)$/, 'The cable was removed, but cleaning up some servers failed: $1'],
  [/^کلید سرور مادر از سرور پاک نشد: ([\s\S]*)$/, "The mother server's key was not removed from the server: $1"],

  [/^پورت "(.*)" کابل «(.*)» برای تانل کرنلی معتبر نیست \(عدد، بازه مثل 2000-3000 یا تغییر شماره مثل 37826:37825\)$/, 'Port "$1" of the cable to "$2" is not valid for a kernel tunnel (a number, a range like 2000-3000, or a remap like 37826:37825)'],

  // a server's new IP
  [/^سرور روی IP تازه در دسترس نیست: سرور روی این IP کلید میزبان دیگری دارد؛.*$/, 'A different host key answers at this IP: either the IP is wrong or the server was reinstalled (then add it again with its password)'],
  [/^سرور روی IP تازه در دسترس نیست: (.*)$/, 'The server does not answer at the new IP: $1'],
  [/^IP ذخیره شد ولی ساخت دوبارهٔ بعضی تانل‌ها ناموفق بود: (.*)$/, 'The IP was saved, but rebuilding some tunnels failed: $1'],
  [/^ساخت دوبارهٔ بعضی تانل‌ها ناموفق بود: (.*)$/, 'Rebuilding some tunnels failed: $1'],

  // editing a cable
  [/^پورت "(.*)" برای تانل کرنلی معتبر نیست \(عدد، بازه مثل 2000-3000 یا تغییر شماره مثل 37826:37825\)$/, 'Port "$1" is not valid for a kernel tunnel (a number, a range like 2000-3000, or a remap like 37826:37825)'],
  [/^پورت (\d+) روی سرور ایران از قبل به «(.*)» منتقل می‌شود؛ برای هر لوکیشن پورت جدا بدهید \(مثلاً 37826:37825\)$/, 'Port $1 on the Iran server already goes to "$2"; give each location ports of its own (for example 37826:37825)'],
  [/^پریست "(.*)" شناخته نیست$/, 'Unknown preset "$1"'],
  [/^برداشتن تانل قبلی ناموفق بود: (.*)$/, 'Removing the old tunnel failed: $1'],

  // step labels of tunnel builds
  [/^این سرور \((.+)\)$/, 'This server ($1)'],
  [/^سرور مقابل \((.+)\)$/, 'Remote server ($1)'],
];
