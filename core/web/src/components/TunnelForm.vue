<script setup>
import { ref, reactive, computed, onMounted } from 'vue';
import { api } from '../lib/api.js';
import { fail, toast } from '../lib/toast.js';
import { randomKey, split } from '../lib/format.js';
import { TRANSPORTS, LISTENERS, PRESETS, MUX_VERSIONS } from '../lib/constants.js';
import SegControl from './SegControl.vue';
import FormField from './FormField.vue';
import AppIcon from './AppIcon.vue';
import KernelForm from './KernelForm.vue';
import MultiForm from './MultiForm.vue';
import { store } from '../lib/store.js';
import { t } from '../lib/i18n.js';

// Create / edit form. Which fields show depends on two choices: which side
// this server is (Iran or abroad) and which side opens the connections.
const props = defineProps({ id: { type: String, default: '' }, kind: { type: String, default: '' } });
const emit = defineEmits(['done', 'cancel']);
const editing = computed(() => !!props.id);
const loading = ref(editing.value);
const saving = ref(false);
let base = {};

const f = reactive({
  name: '', mode: 'client', direction: 'normal', psk: randomKey(), ports: '', udp: '', preset: 'balance',
  targets: '127.0.0.1', sni: 'www.cloudflare.com', fp: 'chrome', carriers: 2, wsPath: '', hostHdr: '', httpListen: '',
  dataSh: 10, parSh: 3, proxy: false, decoy: '', grpcSvc: '',
  sshUser: '', sshPass: '', sshKey: '', sshPort: 22, sshRemote: '',
  tlsFrag: false, shape: false, rotate: 0, mux: 1,
});
if (props.kind === 'kernel') f.direction = 'kernel';
const endpoints = ref([{ transport: 'tls', server: '' }]);
// a kernel tunnel (ESP / MOI) and a multi-location tunnel (one Iran server to
// several Kharej servers) are built by forms of their own, so they are offered
// only when creating
const isKernel = computed(() => f.direction === 'kernel');
const isMulti = computed(() => f.direction === 'multi');
const DIRECTIONS = computed(() => (editing.value
  ? [['normal', t('مستقیم')], ['reverse', t('معکوس')]]
  : [['normal', t('مستقیم')], ['reverse', t('معکوس')], ['kernel', t('کرنلی')], ['multi', t('چند لوکیشن')]]));
const DIR_HELP = {
  normal: 'سرور ایران به خارج وصل می‌شود (تانل مستقیم).',
  reverse: 'سرور خارج به ایران وصل می‌شود؛ روی خارج هیچ پورت ورودی لازم نیست.',
  kernel: 'تانل در سطح کرنل (ESP / MOI) بدون رله؛ کم‌سربارترین مسیر. هر دو سر خودکار با SSH برپا می‌شوند.',
  multi: 'یک سرور ایران به چند سرور خارج؛ هر لوکیشن پورت‌های خودش را دارد و همه یک‌جا روی سرورها ساخته می‌شوند.',
};
const selfHost = (store.session && store.session.public_host) || '';
const portOf = (addr) => (addr || '').split(':').pop();
const listeners = ref(LISTENERS.map(([key, tr, label, def, bind]) => ({ key, tr, label, bind, on: key === 'listen', port: def })));

const dials = computed(() => (f.mode === 'client') !== (f.direction === 'reverse'));
const sshInUse = computed(() => dials.value
  ? endpoints.value.some((e) => e.transport === 'ssh')
  : listeners.value.some((l) => l.key === 'ssh_listen' && l.on));

function load(c) {
  base = c;
  Object.assign(f, {
    mode: c.mode || 'client', direction: c.reverse ? 'reverse' : 'normal', psk: c.psk || randomKey(),
    ports: (c.ports || []).join(', '), udp: (c.udp_ports || []).join(', '), preset: c.preset || 'balance',
    targets: c.targets || '127.0.0.1', sni: c.server_name || 'www.cloudflare.com', fp: c.fingerprint || 'chrome',
    carriers: c.carriers || 2, wsPath: c.ws_path || '', hostHdr: c.host || '', httpListen: c.http_listen || '',
    dataSh: c.kcp_data_shards ?? 10, parSh: c.kcp_parity_shards ?? 3, proxy: !!c.proxy_protocol, decoy: c.decoy || '',
    grpcSvc: c.grpc_service || '', sshUser: c.ssh_user || '', sshPass: c.ssh_password || '', sshKey: c.ssh_key || '',
    sshPort: c.ssh_port || 22, sshRemote: c.ssh_remote || '', tlsFrag: !!c.tls_fragment, shape: !!c.shape, rotate: c.rotate_sec || 0,
    mux: c.mux_version || 1,
  });
  const eps = c.endpoints && c.endpoints.length ? c.endpoints : c.server ? [{ transport: c.transport || 'tls', server: c.server }] : [{ transport: 'tls', server: '' }];
  endpoints.value = eps.map((e) => ({ transport: e.transport || 'tls', server: e.server || '' }));
  for (const l of listeners.value) {
    l.on = !!c[l.key];
    if (c[l.key] && l.tr !== 'icmp') l.port = Number(portOf(c[l.key])) || l.port;
  }
}

function build() {
  const c = Object.assign({}, base, {
    mode: f.mode, reverse: f.direction === 'reverse', psk: f.psk.trim(), preset: f.preset,
    ports: split(f.ports), udp_ports: split(f.udp),
    server_name: f.sni.trim(), fingerprint: f.fp, carriers: Number(f.carriers) || 2,
    ws_path: f.wsPath.trim(), host: f.hostHdr.trim(), http_listen: f.httpListen.trim(),
    kcp_data_shards: Number(f.dataSh), kcp_parity_shards: Number(f.parSh),
    proxy_protocol: f.proxy, insecure_tls: true, decoy: f.decoy.trim(), grpc_service: f.grpcSvc.trim(),
    tls_fragment: f.tlsFrag, shape: f.shape, rotate_sec: Number(f.rotate) || 0,
    mux_version: Number(f.mux) === 2 ? 2 : 0,
  });
  delete c.server; delete c.transport;
  for (const k of ['ssh_user', 'ssh_password', 'ssh_key', 'ssh_port', 'ssh_remote']) delete c[k];
  if (dials.value) {
    c.endpoints = endpoints.value.filter((e) => e.server.trim()).map((e) => ({ transport: e.transport, server: e.server.trim() }));
    for (const l of listeners.value) delete c[l.key];
    delete c.http_listen;
  } else {
    delete c.endpoints;
    for (const l of listeners.value) {
      if (!l.on) { delete c[l.key]; continue; }
      // ICMP has no port: its listener is just the address to bind
      c[l.key] = l.tr === 'icmp' ? l.bind : l.bind + ':' + l.port;
    }
  }
  // the SSH login travels with the config: the dialing side uses it; the
  // listening side keeps it to hand to a peer that dials in over SSH
  if (sshInUse.value) {
    c.ssh_user = f.sshUser.trim();
    if (f.sshPass) c.ssh_password = f.sshPass;
    if (f.sshKey.trim()) c.ssh_key = f.sshKey.trim();
    c.ssh_port = Number(f.sshPort) || 22;
    if (f.sshRemote.trim()) c.ssh_remote = f.sshRemote.trim();
  }
  if (f.mode === 'server') c.targets = f.targets.trim() || '127.0.0.1'; else delete c.targets;
  for (const k of Object.keys(c)) if (c[k] === '' || (Array.isArray(c[k]) && !c[k].length)) delete c[k];
  if (!c.mux_version) delete c.mux_version;
  return c;
}
const preview = computed(() => JSON.stringify(build(), null, 2));

async function submit() {
  if (isKernel.value || isMulti.value) return; // those builders have their own buttons
  saving.value = true;
  try {
    const config = build();
    if (editing.value) {
      await api('PUT', '/api/tunnels/' + encodeURIComponent(props.id), { config });
      toast(t('ذخیره شد و تانل ری‌استارت شد'), 'ok');
      emit('done', props.id);
    } else {
      const n = f.name.trim();
      await api('POST', '/api/tunnels', { name: n, config });
      toast(t('تانل ساخته شد. حالا سمت مقابل را راه‌اندازی کنید.'), 'ok');
      emit('done', n);
    }
  } catch (e) { fail(e); }
  saving.value = false;
}

onMounted(async () => {
  if (!editing.value) return;
  try { load((await api('GET', '/api/tunnels/' + encodeURIComponent(props.id))).config || {}); }
  catch (e) { fail(e); emit('cancel'); }
  loading.value = false;
});
</script>

<template>
  <div v-if="loading" class="empty"><span class="spin"></span></div>
  <form v-else class="tform" @submit.prevent="submit">
    <div :class="isKernel || isMulti ? 'kcore' : 'grid-2'">
      <div class="card">
        <div class="card-head"><h3><AppIcon name="tunnel" /> {{ $t('پایه') }}</h3><span class="tag">CORE</span></div>
        <FormField v-if="!editing && !isKernel && !isMulti" :label="$t('نام تانل')" :hint="$t('حروف انگلیسی، عدد، - و _')">
          <input v-model="f.name" type="text" dir="ltr" placeholder="uk-1" required />
        </FormField>
        <FormField v-if="!isMulti" :label="$t('این سرور کجاست؟')" :hint="f.mode === 'client' ? $t('این سرور در ایران است: کاربران به پورت‌های آن وصل می‌شوند.') : $t('این سرور خارج است: سرویس‌ها (xray) روی آن اجرا می‌شوند.')">
          <SegControl v-model="f.mode" :options="[['client', $t('ایران (کاربران)')], ['server', $t('خارج (سرویس‌ها)')]]" />
        </FormField>
        <FormField :label="$t('نوع تانل')" :hint="$t(DIR_HELP[f.direction])">
          <SegControl v-model="f.direction" :options="DIRECTIONS" />
        </FormField>
        <template v-if="!isMulti">
        <FormField :label="$t('پورت‌های TCP')" :hint="isKernel ? $t('پورت‌هایی که از تانل کرنلی رد می‌شوند؛ مثلاً 443, 8443 (خالی = همه)') : (f.mode === 'client' ? $t('مثال: 443 یا 443:2053 (تغییر شماره) یا 8000-8010 (رنج) یا 443=10.0.0.5:2096 (مقصد دلخواه)') : $t('پورت‌های سرویس روی این سرور'))">
          <input v-model="f.ports" type="text" dir="ltr" placeholder="443, 8443:2053, 8000-8010" />
        </FormField>
        <FormField :label="$t('پورت‌های UDP')" :hint="$t('برای WireGuard، OpenVPN-UDP و مانند آن؛ خالی یعنی بدون UDP')">
          <input v-model="f.udp" type="text" dir="ltr" placeholder="51820" />
        </FormField>
        <FormField v-if="!isKernel" :label="$t('پریست سرعت')" :hint="$t('توربو و بالاتر = بافر و حامل بیشتر؛ مصرف رم و پهنای باند بیشتر')">
          <select v-model="f.preset"><option v-for="[v, l] in PRESETS" :key="v" :value="v">{{ $t(l) }}</option></select>
        </FormField>
        <FormField v-if="f.mode === 'server' && !isKernel" :label="$t('مقصد سرویس‌ها')" :hint="$t('معمولاً 127.0.0.1 (xray روی همین سرور)')">
          <input v-model="f.targets" type="text" dir="ltr" />
        </FormField>
        <FormField v-if="!isKernel" :label="$t('کلید تانل (PSK)')" :hint="$t('هر دو سمت باید یک کلید داشته باشند؛ دستور سمت مقابل آن را خودکار منتقل می‌کند.')">
          <div class="cmd"><input v-model="f.psk" type="text" dir="ltr" required /><button type="button" class="btn icon-only" :title="$t('کلید جدید')" @click="f.psk = randomKey()"><AppIcon name="key" /></button></div>
        </FormField>
        </template>
      </div>

      <div v-if="!isKernel && !isMulti" class="card">
        <div class="card-head"><h3><AppIcon name="net" /> {{ $t('ترنسپورت') }}</h3><span class="tag">TRANSPORT</span></div>
        <template v-if="dials">
          <div class="section-title">{{ $t('مسیرهای اتصال') }}</div>
          <div class="hint">{{ $t('به ترتیب امتحان می‌شوند؛ اگر اولی قطع شود، تانل خودکار به بعدی می‌رود.') }}</div>
          <div v-for="(e, i) in endpoints" :key="i" class="ep-row">
            <select v-model="e.transport"><option v-for="(v, k) in TRANSPORTS" :key="k" :value="k">{{ v.name }}</option></select>
            <input v-model="e.server" type="text" dir="ltr" :placeholder="e.transport === 'icmp' ? '1.2.3.4' : '1.2.3.4:443'" />
            <button type="button" class="btn danger small icon-only" :title="$t('حذف')" :disabled="endpoints.length < 2" @click="endpoints.splice(i, 1)"><AppIcon name="trash" /></button>
          </div>
          <button type="button" class="btn small" @click="endpoints.push({ transport: 'kcp', server: '' })"><AppIcon name="plus" /> {{ $t('افزودن مسیر پشتیبان') }}</button>
        </template>
        <template v-else>
          <div class="section-title">{{ $t('پورت‌های شنود این سرور') }}</div>
          <div class="hint">{{ $t('سمت مقابل به این پورت‌ها وصل می‌شود. هر ترنسپورتی که روشن کنید، آن سمت می‌تواند از آن استفاده کند.') }}</div>
          <div v-for="l in listeners" :key="l.key" class="lst-row">
            <label class="check"><input v-model="l.on" type="checkbox" /> {{ $t(l.label) }}</label>
            <input v-if="l.tr !== 'icmp'" v-model.number="l.port" type="number" min="1" max="65535" dir="ltr" :disabled="!l.on" />
          </div>
        </template>
        <details class="adv tr-help"><summary>{{ $t('راهنمای ترنسپورت‌ها') }}</summary>
          <div class="seg-help"><div v-for="(v, k) in TRANSPORTS" :key="k"><b>{{ v.name }}: </b>{{ $t(v.help) }}</div></div>
        </details>
      </div>
    </div>

    <KernelForm v-if="isKernel" :role="f.mode === 'client' ? 'IRAN' : 'KHAREJ'" :self-host="selfHost" :ports="f.ports" :udp-ports="f.udp"
      @done="emit('done', '', 'kernel')" @cancel="emit('cancel')" />
    <MultiForm v-else-if="isMulti" @done="(n, kind) => emit('done', n, kind)" @cancel="emit('cancel')" />

    <template v-else>
    <div v-if="sshInUse" class="card">
      <div class="card-head"><h3><AppIcon name="key" /> {{ $t('ورود SSH') }}</h3><span class="tag">SSH LOGIN</span></div>
      <p class="hint">{{ $t('برای ترنسپورت SSH: حامل از طریق sshd سرور مقابل عبور می‌کند. این اطلاعات ورودِ sshd سمت شنونده است (همان سروری که ssh_listen دارد).') }}</p>
      <div class="row">
        <FormField :label="$t('کاربر SSH')"><input v-model="f.sshUser" type="text" dir="ltr" placeholder="root" /></FormField>
        <FormField :label="$t('رمز SSH')" :hint="$t('یا به‌جای آن، کلید خصوصی را بگذارید')"><input v-model="f.sshPass" type="password" dir="ltr" autocomplete="new-password" /></FormField>
      </div>
      <FormField :label="$t('کلید خصوصی SSH')"><textarea v-model="f.sshKey" dir="ltr" :placeholder="$t('-----BEGIN OPENSSH PRIVATE KEY----- (به‌جای رمز، اختیاری)')"></textarea></FormField>
      <div class="row">
        <FormField :label="$t('پورت SSH')"><input v-model.number="f.sshPort" type="number" min="1" max="65535" dir="ltr" /></FormField>
        <FormField :label="$t('آدرس رلهٔ لوپ‌بک سمت مقابل')" :hint="$t('پیش‌فرض 127.0.0.1:8710؛ باید با ssh_listen آن سرور یکی باشد')"><input v-model="f.sshRemote" type="text" dir="ltr" placeholder="127.0.0.1:8710" /></FormField>
      </div>
    </div>

    <div class="card">
      <details class="adv"><summary>{{ $t('تنظیمات پیشرفته') }}</summary>
        <div class="row">
          <FormField :label="$t('نام در دست‌دهی TLS (SNI)')"><input v-model="f.sni" type="text" dir="ltr" /></FormField>
          <FormField :label="$t('اثرانگشت مرورگر')"><select v-model="f.fp"><option v-for="x in ['chrome', 'firefox', 'safari', 'edge', 'random']" :key="x" :value="x">{{ x }}</option></select></FormField>
          <FormField :label="$t('تعداد حامل‌های موازی')"><input v-model.number="f.carriers" type="number" min="1" max="16" /></FormField>
        </div>
        <FormField :label="$t('مالتی‌پلکسر')" :hint="$t('در نسخهٔ ۲ کاربر کند بقیهٔ کاربران همان حامل را کند نمی‌کند. دستور «راه‌اندازی سمت مقابل» همین نسخه را به آن سمت می‌دهد؛ اگر سمت مقابل را دستی ساخته‌اید، آن را هم عوض کنید.')">
          <select v-model.number="f.mux"><option v-for="[v, n] in MUX_VERSIONS" :key="v" :value="v">{{ $t(n) }}</option></select>
        </FormField>
        <div class="row">
          <FormField :label="$t('مسیر WebSocket')" :hint="$t('برای ترنسپورت WebSocket و عبور از CDN')"><input v-model="f.wsPath" type="text" dir="ltr" placeholder="/secret-path" /></FormField>
          <FormField :label="$t('هدر Host')" :hint="$t('فقط وقتی از CDN استفاده می‌کنید')"><input v-model="f.hostHdr" type="text" dir="ltr" placeholder="cdn.example.com" /></FormField>
          <FormField :label="$t('شنود HTTP برای CDN')" :hint="$t('فقط سمت سرور، برای CDN با حالت Flexible')"><input v-model="f.httpListen" type="text" dir="ltr" placeholder="0.0.0.0:8880" /></FormField>
        </div>
        <div class="row">
          <FormField :label="$t('KCP: شاردهای داده')"><input v-model.number="f.dataSh" type="number" min="0" max="128" /></FormField>
          <FormField :label="$t('KCP: شاردهای توازن (FEC)')" :hint="$t('۰ = بدون FEC (UDP سبک)؛ بیشتر = تحمل افت بیشتر، مصرف پهنای باند بیشتر')"><input v-model.number="f.parSh" type="number" min="0" max="128" /></FormField>
          <FormField :label="$t('نام سرویس gRPC')" :hint="$t('مسیر تماس /<سرویس>/Tun؛ خالی یعنی GunService')"><input v-model="f.grpcSvc" type="text" dir="ltr" placeholder="GunService" /></FormField>
        </div>
        <FormField :label="$t('سایت فریب (decoy)')" :hint="$t('اتصال ناشناس و probe به این آدرس فرستاده می‌شود تا پورت مثل یک سایت واقعی پاسخ دهد؛ فقط سمت شنونده')">
          <input v-model="f.decoy" type="text" dir="ltr" placeholder="example.com:443" />
        </FormField>
        <label class="check"><input v-model="f.proxy" type="checkbox" /> {{ $t('ارسال IP واقعی کاربر به سرویس (PROXY v2) — باید در xray هم فعال شود') }}</label>
        <div class="section-title">{{ $t('ضد DPI و استتار') }}</div>
        <label class="check"><input v-model="f.tlsFrag" type="checkbox" /> {{ $t('قطعه‌قطعه‌کردن ClientHello (شکستن SNI؛ فقط سمتی که وصل می‌شود)') }}</label>
        <label class="check"><input v-model="f.shape" type="checkbox" /> {{ $t('شکل‌دهی ترافیک: پر کردن فریم‌های Stealth با بایت تصادفی') }}</label>
        <FormField :label="$t('چرخش اتصال (ثانیه)')" :hint="$t('۰ = خاموش؛ وگرنه هر چند ثانیه حامل‌ها نو می‌شوند (حداقل ۳۰؛ فقط سمتی که وصل می‌شود)')">
          <input v-model.number="f.rotate" type="number" min="0" dir="ltr" />
        </FormField>
      </details>
    </div>

    <div class="card">
      <details class="adv"><summary>{{ $t('پیش‌نمایش کانفیگ') }}</summary><pre class="code">{{ preview }}</pre></details>
    </div>

    <div class="form-actions">
      <button class="btn primary" type="submit" :disabled="saving">
        <span v-if="saving" class="spin"></span><AppIcon v-else :name="editing ? 'edit' : 'plus'" />
        {{ editing ? $t('ذخیره و ری‌استارت') : $t('ساخت تانل') }}
      </button>
      <button type="button" class="btn" @click="emit('cancel')">{{ $t('انصراف') }}</button>
    </div>
    </template>
  </form>
</template>

<style>
.tform .card + .card, .tform .grid-2 + .card { margin-top: 14px; }
.ep-row { display: grid; grid-template-columns: 150px 1fr auto; gap: 8px; margin: 10px 0; }
.lst-row { display: grid; grid-template-columns: 1fr 110px; gap: 10px; align-items: center; }
.lst-row .check { margin: 6px 0; }
.tr-help { margin-top: 14px; }
.form-actions { display: flex; gap: 8px; margin-top: 16px; }
.kcore > .card { max-width: none; }
</style>
