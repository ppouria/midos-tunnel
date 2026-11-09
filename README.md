<div align="center">

<img src="docs/logo.png" width="140" alt="Midos Tunnel logo">

# Midos Tunnel

**Fast, stealthy Iran ⇄ abroad tunnels with a complete web panel**<br>
Direct, reverse and kernel tunnels · fleet management from one "mother" server · 3D network designer

[![Release](https://img.shields.io/github/v/release/1Rootzen/midos-tunnel?style=flat-square&color=3d45aa)](https://github.com/1Rootzen/midos-tunnel/releases/latest)
[![Platform](https://img.shields.io/badge/platform-linux%20amd64%20%7C%20arm64-3d45aa?style=flat-square)](#requirements)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white)](core/go.mod)
[![License](https://img.shields.io/badge/license-source--available-8f96f0?style=flat-square)](LICENSE)

[**English**](#english) · [**فارسی**](#فارسی)

<img src="docs/screenshots/en/dashboard.png" alt="Midos Tunnel dashboard" width="920">

</div>

## Install in one line

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/install.sh)
```

Run it as root on a Linux server. It asks nothing, shows a progress bar, and ends with the panel link, user name and password.

---

<a id="english"></a>

## English

### Highlights

| | |
|---|---|
| 🧭 **Web panel** | English and Persian (switch any time; English by default), installs in one line. Random port and secret path, captcha login, self-signed TLS or a free Let's Encrypt certificate from Settings. |
| 🔁 **Three tunnel types** | **Direct** (the Iran server dials out), **reverse** (the abroad server dials in — no open port needed abroad) and **kernel** tunnels (ESP / ESP over UDP / MOI / FOU / full IPsec) with up to 32 abroad servers per Iran hub. |
| 🛰️ **10 transports** | TLS with a real browser fingerprint, WebSocket and gRPC (CDN friendly), plain TCP, Stealth (random bytes on the wire), KCP + FEC and QUIC over UDP, DNS-shaped UDP, ICMP (ping only) and SSH. |
| ⚡ **Built for bad links** | Several parallel carriers per tunnel, fallback endpoints, speed presets, UDP forwarding and PROXY protocol v2 so services see the user's real IP. A user connection that stops reading is closed after two minutes, so it cannot freeze the carrier for everyone else, and multiplexer version 2 gives every user its own flow control. |
| 🖥️ **Mother server & fleet** | Add Iran and abroad servers by IP and root password (used once). Each one is updated, gets every prerequisite, the tunnel binary and a benchmark automatically, then shows live CPU / RAM / disk / network on its card. Works over routes that cut SSH after a few seconds: every step runs on the server in the background and is followed with short connections, and large files are downloaded by the server instead of pushed. When a provider changes a server's IP, **Change IP** checks it is the same server at the new address and rebuilds all of its tunnels. |
| 🗺️ **3D network designer** | Draw servers and cables (one-to-one or one-to-many) and apply them; the tunnels are built on both ends for you. A **multi-location** tunnel (one Iran server to several abroad servers, each location with its own ports and a port map showing which Iran port goes to which server) can also be built straight from the new-tunnel form, and every built tunnel has an **Edit** button to change its ports, transport or type later. |
| 📊 **Operations** | Traffic for every tunnel (kernel and server-to-server ones included) with usage limits, health checks with one-click repair, route test from any of your servers, per-country benchmark, audit log, Telegram alerts, watchdog webhook, backup / restore. |
| ⬆️ **Safe updates** | One-click update from GitHub releases with SHA-256 verification and rollback. |

### Screenshots

| Fleet (mother server) | 3D network designer |
|---|---|
| <img src="docs/screenshots/en/servers.png" alt="Servers"> | <img src="docs/screenshots/en/topology.png" alt="Network designer"> |
| **Tunnels** | **New tunnel** |
| <img src="docs/screenshots/en/tunnels.png" alt="Tunnels"> | <img src="docs/screenshots/en/new-tunnel.png" alt="New tunnel"> |
| **Per-country benchmark** | **Settings** |
| <img src="docs/screenshots/en/benchmark.png" alt="Benchmark"> | <img src="docs/screenshots/en/settings.png" alt="Settings"> |

<p align="center"><img src="docs/screenshots/en/mobile.png" alt="Mobile" width="260"></p>

<a id="requirements"></a>

### Requirements

- Linux with systemd: Ubuntu 20.04+ or Debian 11+ recommended (Rocky / Alma / CentOS work for the panel and the direct and reverse tunnels)
- amd64 or arm64, root access
- Outbound access to `github.com` for install and updates (or a mirror, see below)

### Install, update, uninstall

```bash
# install, or update to the latest release (an existing panel keeps its address and password)
bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/install.sh)

# the same installer from the release, if raw.githubusercontent.com is blocked
bash <(curl -fsSL https://github.com/1Rootzen/midos-tunnel/releases/latest/download/install.sh)

# remove everything Midos Tunnel installed (preview first with --dry-run)
bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/uninstall.sh)
```

The installer installs the system packages, downloads the `midos-tunnel` binary and checks its SHA-256, prepares the kernel-tunnel base (modules, BBR, forwarding) and starts the panel as the `midos-tunnel-panel` service. It opens the panel port in `ufw` / `firewalld` if one is active. It never flushes firewall tables and never touches x-ui, xray, SSH or other software.

| Variable | Effect |
|---|---|
| `MIDOS_VERSION=v2.6.7` | install a specific release instead of the latest |
| `MIDOS_MIRROR=https://…` | download the release files from a mirror |
| `MIDOS_PANEL_PORT=2053` | panel port on first install (random otherwise) |
| `MIDOS_NO_KERNEL=1` | skip the kernel-tunnel base |
| `MIDOS_RESET_PASSWORD=1` | on an update, also print a new panel password |

Example: `MIDOS_PANEL_PORT=2053 bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/install.sh)`

Useful commands on the server:

```bash
midos-tunnel panel url       # show the panel address
midos-tunnel panel passwd    # set a new random password
systemctl status midos-tunnel-panel
Midos-tunnel.sh            # interactive kernel-tunnel menu
```

> If your provider has a cloud firewall, open the panel port there too.

### Quick start

1. **Install** on one server. It becomes the *mother server* that manages the others.
2. **Servers → Add server**: enter the IP and root password of each Iran or abroad server. The mother logs in once, installs its own SSH key and never stores the password; the server is then updated, prepared and benchmarked automatically.
3. **Network designer**: drag a cable between an Iran server and an abroad server, choose **direct**, **reverse** or **kernel**, then **Apply**.
4. Or build a single tunnel by hand from **Tunnels → New tunnel** on the Iran server; the panel prints the one command to run on the other side.

### Build from source

Requires Go 1.26+ and Node.js 20+.

```bash
git clone https://github.com/1Rootzen/midos-tunnel.git
cd midos-tunnel/core/web && npm ci && npm run build      # builds the panel into core/panelweb
cd .. && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o midos-tunnel .
go test ./...
```

### Repository layout

| Path | What it is |
|---|---|
| `core/` | the `midos-tunnel` tunnel engine and web panel (Go) |
| `core/web/` | panel front end (Vue 3 + Vite) |
| `install.sh` / `uninstall.sh` | one-line installer and surgical uninstaller |
| `Midos-tunnel.sh` | kernel-tunnel manager (also used by the panel) |
| `docs/` | logo and screenshots |

### Sponsor banner and license

The panel shows a small sponsor banner at the top; that is how this free project is funded. The code is source-available under the [LICENSE](LICENSE): you may use, modify and redistribute it, as long as the sponsor banner stays in place and visible in anything you redistribute or host for others.

### Contributors

- [@ppouria](https://github.com/ppouria) (Pouria): the stall guard that keeps one stuck user from freezing a relay tunnel's carrier, and multiplexer version 2 with per-user flow control (2.6.7)

Pull requests are welcome.

---

<a id="فارسی"></a>

<div dir="rtl">

## فارسی

### امکانات

| | |
|---|---|
| 🧭 **پنل تحت وب** | دو زبانهٔ انگلیسی و فارسی (پیش‌فرض انگلیسی؛ با یک دکمه عوض می‌شود) و نصب یک‌خطی. پورت تصادفی و مسیر مخفی، ورود با کد امنیتی، گواهی self-signed یا گواهی رایگان Let's Encrypt از بخش تنظیمات. |
| 🔁 **سه نوع تانل** | **مستقیم** (سرور ایران به خارج وصل می‌شود)، **معکوس** (سرور خارج به ایران وصل می‌شود؛ روی خارج پورت باز لازم نیست) و **کرنلی** (ESP، ESP روی UDP، MOI، FOU، IPsec کامل) با حداکثر ۳۲ سرور خارج برای هر سرور ایران. |
| 🛰️ **۱۰ ترنسپورت** | TLS با اثرانگشت واقعی مرورگر، WebSocket و gRPC (سازگار با CDN)، TCP ساده، Stealth (روی سیم فقط بایت تصادفی)، KCP+FEC و QUIC روی UDP، UDP شبیه DNS، ICMP (جایی که فقط ping باز است) و SSH. |
| ⚡ **مناسب مسیرهای ضعیف** | چند حامل موازی برای هر تانل، مسیرهای پشتیبان، پریست سرعت، انتقال UDP و پروتکل PROXY v2 تا سرویس‌ها IP واقعی کاربر را ببینند. اتصال کاربری که دیگر داده نمی‌خواند بعد از دو دقیقه بسته می‌شود تا حامل را برای بقیه از کار نیندازد، و نسخهٔ ۲ مالتی‌پلکسر به هر کاربر کنترل جریان جدا می‌دهد. |
| 🖥️ **سرور مادر و ناوگان** | سرورهای ایران و خارج را با IP و رمز root (فقط یک بار) اضافه کنید. هر سرور خودکار به‌روز می‌شود، همهٔ پیش‌نیازها و باینری تانل رویش نصب و بنچمارک می‌شود و منابع زنده‌اش (CPU، RAM، دیسک، شبکه) روی کارتش دیده می‌شود. روی مسیرهایی که SSH را بعد از چند ثانیه قطع می‌کنند هم کار می‌کند: هر مرحله در پس‌زمینهٔ سرور اجرا و با اتصال‌های کوتاه دنبال می‌شود و فایل‌های بزرگ را خود سرور دانلود می‌کند. وقتی شرکت ارائه‌دهنده IP سروری را عوض کرد، **تغییر IP** بررسی می‌کند همان سرور روی آدرس تازه است و همهٔ تانل‌هایش را دوباره می‌سازد. |
| 🗺️ **طراحی شبکهٔ سه‌بعدی** | سرورها و کابل‌ها را بکشید (دو به دو یا یک به چند) و اعمال کنید؛ تانل روی هر دو طرف ساخته می‌شود. تانل **چند لوکیشن** (یک سرور ایران به چند سرور خارج، هر لوکیشن با پورت‌های خودش و نقشهٔ پورتی که نشان می‌دهد هر پورت ایران به کدام سرور می‌رود) را می‌توانید مستقیم از فرم تانل جدید هم بسازید و هر تانل ساخته‌شده دکمهٔ **ویرایش** دارد تا بعداً پورت، ترنسپورت یا نوعش را عوض کنید. |
| 📊 **مدیریت و پایش** | ترافیک همهٔ تانل‌ها (از جمله کرنلی و بین سرورها) و سقف مصرف، بررسی سلامت با تعمیر یک‌کلیکی، تست مسیر از هر کدام از سرورها، بنچمارک کشوربه‌کشور، گزارش ممیزی، هشدار تلگرام، وبهوک واچ‌داگ، پشتیبان‌گیری و بازیابی. |
| ⬆️ **به‌روزرسانی امن** | به‌روزرسانی یک‌کلیکی از ریلیزهای گیت‌هاب با بررسی SHA-256 و امکان بازگشت به نسخهٔ قبل. |

### تصاویر

| ناوگان (سرور مادر) | طراحی شبکهٔ سه‌بعدی |
|---|---|
| <img src="docs/screenshots/fa/servers.png" alt="سرورها"> | <img src="docs/screenshots/fa/topology.png" alt="طراحی شبکه"> |
| **تانل‌ها** | **تانل جدید** |
| <img src="docs/screenshots/fa/tunnels.png" alt="تانل‌ها"> | <img src="docs/screenshots/fa/new-tunnel.png" alt="تانل جدید"> |
| **بنچمارک کشوربه‌کشور** | **تنظیمات** |
| <img src="docs/screenshots/fa/benchmark.png" alt="بنچمارک"> | <img src="docs/screenshots/fa/settings.png" alt="تنظیمات"> |

<p align="center"><img src="docs/screenshots/fa/dashboard.png" alt="داشبورد" width="640"> <img src="docs/screenshots/fa/mobile.png" alt="موبایل" width="200"></p>

### نصب، به‌روزرسانی و حذف

با کاربر root روی سرور لینوکسی اجرا کنید:

```bash
# نصب یا به‌روزرسانی به آخرین نسخه (پنل موجود آدرس و رمزش را نگه می‌دارد)
bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/install.sh)

# اگر raw.githubusercontent.com در دسترس نبود، همان نصب‌کننده از ریلیز
bash <(curl -fsSL https://github.com/1Rootzen/midos-tunnel/releases/latest/download/install.sh)

# حذف کامل (اول با --dry-run پیش‌نمایش بگیرید)
bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/uninstall.sh)
```

نصب‌کننده بسته‌های سیستمی را نصب می‌کند، باینری `midos-tunnel` را دانلود و SHA-256 آن را بررسی می‌کند، پایهٔ تانل کرنلی (ماژول‌ها، BBR، forwarding) را آماده می‌کند و پنل را به‌صورت سرویس `midos-tunnel-panel` راه می‌اندازد. اگر `ufw` یا `firewalld` فعال باشد، پورت پنل را باز می‌کند. هرگز جدول‌های فایروال را پاک نمی‌کند و به x-ui، xray، SSH یا نرم‌افزارهای دیگر دست نمی‌زند. در پایان آدرس پنل، نام کاربری و رمز را نشان می‌دهد.

| متغیر | کاربرد |
|---|---|
| `MIDOS_VERSION=v2.6.7` | نصب یک نسخهٔ مشخص به‌جای آخرین نسخه |
| `MIDOS_MIRROR=https://…` | دانلود فایل‌های ریلیز از یک میرور |
| `MIDOS_PANEL_PORT=2053` | پورت پنل در نصب اول (در غیر این صورت تصادفی) |
| `MIDOS_NO_KERNEL=1` | آماده نکردن پایهٔ تانل کرنلی |
| `MIDOS_RESET_PASSWORD=1` | هنگام به‌روزرسانی، رمز جدید پنل هم ساخته و نمایش داده شود |

دستورهای کاربردی روی سرور:

```bash
midos-tunnel panel url       # نمایش آدرس پنل
midos-tunnel panel passwd    # ساخت رمز جدید
systemctl status midos-tunnel-panel
Midos-tunnel.sh            # منوی تانل کرنلی
```

> اگر سرویس‌دهنده‌تان فایروال ابری دارد، پورت پنل را آن‌جا هم باز کنید.

### شروع سریع

1. پنل را روی یک سرور **نصب** کنید؛ این سرور «سرور مادر» می‌شود و بقیه را مدیریت می‌کند.
2. **سرورها ← افزودن سرور**: IP و رمز root هر سرور ایران یا خارج را بدهید. سرور مادر فقط یک بار وارد می‌شود، کلید SSH خودش را نصب می‌کند و رمز را ذخیره نمی‌کند؛ بعد سرور خودکار به‌روز، آماده و بنچمارک می‌شود.
3. **طراحی شبکه**: بین یک سرور ایران و یک سرور خارج کابل بکشید، نوع **مستقیم**، **معکوس** یا **کرنلی** را انتخاب و **اعمال** کنید.
4. یا یک تانل را دستی از **تانل‌ها ← تانل جدید** روی سرور ایران بسازید؛ پنل یک دستور برای اجرا روی طرف مقابل می‌دهد.

### ساخت از سورس

به Go نسخهٔ ۱.۲۶ به بالا و Node.js نسخهٔ ۲۰ به بالا نیاز است:

```bash
git clone https://github.com/1Rootzen/midos-tunnel.git
cd midos-tunnel/core/web && npm ci && npm run build
cd .. && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o midos-tunnel .
```

### بنر حامی و لایسنس

در بالای پنل یک بنر کوچک حامی نمایش داده می‌شود؛ هزینهٔ این پروژهٔ رایگان از همین راه تأمین می‌شود. سورس طبق [LICENSE](LICENSE) در دسترس است: استفاده، تغییر و بازنشر آزاد است، به این شرط که بنر حامی در هر نسخه‌ای که منتشر می‌کنید یا برای دیگران روی سرور اجرا می‌کنید، سر جایش و قابل‌مشاهده بماند.

### مشارکت‌کنندگان

- [@ppouria](https://github.com/ppouria) (پوریا): محافظ گیر کردن جریان که نمی‌گذارد یک کاربر گیرکرده حامل یک تانل رله را از کار بیندازد، و نسخهٔ ۲ مالتی‌پلکسر با کنترل جریان جدا برای هر کاربر (۲.۶.۷)

درخواست‌های ادغام (Pull Request) با خوشحالی پذیرفته می‌شوند.

</div>
