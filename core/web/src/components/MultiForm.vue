<script setup>
import { ref, reactive, computed, onMounted } from 'vue';
import { api } from '../lib/api.js';
import { fail, toast } from '../lib/toast.js';
import { t, fwd } from '../lib/i18n.js';
import { fa } from '../lib/format.js';
import { DEF_PORT } from '../lib/constants.js';
import { nodeName, localPorts, remotePorts, portPairs, UDP_CARRIERS, STEP_ICON } from '../lib/fleet.js';
import FormField from './FormField.vue';
import AppIcon from './AppIcon.vue';
import CableFields from './CableFields.vue';
import AddServerWindow from './AddServerWindow.vue';

// Multi-location: one Iran server tunnelled to several Kharej servers at once.
// Each Kharej server gets a cable of its own with its own forwarded ports; the
// cables land in the network designer, where they can be edited or removed.
const emit = defineEmits(['done', 'cancel']);

const fleet = ref({ self_role: 'none', nodes: [], links: [] });
const f = reactive({ iran: '', kind: 'direct', reverse: false, transport: 'tls', port: 443, preset: 'balance', carriers: 2, mux_version: 1, mode: 'esp', ports: '', udp_ports: '' });
const blank = () => ({ node: '', ports: '', udp: '', port: '' });
const rows = ref([blank()]);
const adding = ref(false);
const busy = ref(false);
const finished = ref(false);
const jobs = ref([]);

const byId = (id) => fleet.value.nodes.find((n) => n.id === id) || {};
const steps = (results) => (results || []).map((s) => (s.ok ? '✅ ' : '❌ ') + s.step + '\n' + (s.error ? s.error + '\n' : '') + (s.output || '').trim()).join('\n\n');

async function load() {
  try { fleet.value = await api('GET', '/api/fleet'); } catch (e) { fail(e); }
}
onMounted(async () => {
  await load();
  if (!f.iran && iranChoices.value.length === 1) f.iran = iranChoices.value[0].id;
});

// the mother can be the Iran end unless it already plays Kharej in the designer
const iranChoices = computed(() => {
  const out = [];
  const self = fleet.value.nodes.find((n) => n.kind === 'self');
  const role = fleet.value.self_role || 'none';
  const selfLinked = fleet.value.links.some((l) => l.a === 'self' || l.b === 'self');
  if (self && (role === 'iran' || (role === 'none' && !selfLinked))) out.push({ id: 'self', name: t('همین سرور ({name})', { name: self.host || nodeName(self) }) });
  for (const n of fleet.value.nodes) if (n.kind === 'iran' && n.state === 'ready') out.push({ id: n.id, name: nodeName(n) + (n.ip ? ' · ' + n.ip : '') });
  return out;
});
const kharejs = computed(() => fleet.value.nodes.filter((n) => n.kind === 'kharej' && n.state === 'ready'));
const hasCable = (id) => fleet.value.links.some((l) => (l.a === f.iran && l.b === id) || (l.b === f.iran && l.a === id));
const takenElsewhere = (id, i) => rows.value.some((r, j) => j !== i && r.node === id);
const kernel = computed(() => f.kind === 'kernel');
// reverse cables all listen on the Iran server, so each needs a port of its own
const perRowPort = computed(() => !kernel.value && f.reverse && f.transport !== 'icmp');
const carrierPort = (r, i) => Number(r.port) || (Number(f.port) || DEF_PORT[f.transport] || 443) + i;
const iranName = computed(() => (iranChoices.value.find((c) => c.id === f.iran) || {}).name || '');

// the cables the chosen Iran server already has, with the ports each takes there
const existing = computed(() => (f.iran ? fleet.value.links.filter((l) => l.a === f.iran || l.b === f.iran) : []).map((l) => ({
  name: nodeName(byId(l.a === f.iran ? l.b : l.a)), ports: l.ports || '', udp: l.udp_ports || '', kind: l.kind,
})));

// the ports each server takes on the Iran server: two locations on one port
// would leave one of them without traffic
function claims() {
  const tcp = new Map(), udp = new Map();
  for (const x of existing.value) {
    for (const p of localPorts(x.ports)) tcp.set(p, x.name);
    for (const p of localPorts(x.udp)) udp.set(p, x.name);
  }
  return { tcp, udp };
}

function check() {
  if (!f.iran) return t('سرور ایران را انتخاب کنید');
  if (!rows.value.some((r) => r.node)) return t('حداقل یک سرور خارج انتخاب کنید');
  const { tcp, udp } = claims();
  const claim = (m, p, who) => {
    const had = m.get(p);
    if (had && had !== who) return t('پورت {p} روی سرور ایران هم برای «{a}» است هم «{b}»', { p, a: had, b: who });
    m.set(p, who);
    return '';
  };
  for (const [i, r] of rows.value.entries()) {
    if (!r.node) continue;
    const who = nodeName(byId(r.node));
    if (!localPorts(r.ports).length && !localPorts(r.udp).length) return t('برای «{name}» حداقل یک پورت وارد کنید', { name: who });
    const msgs = localPorts(r.ports).map((p) => claim(tcp, p, who)).concat(localPorts(r.udp).map((p) => claim(udp, p, who)));
    if (perRowPort.value) msgs.push(claim(UDP_CARRIERS.includes(f.transport) ? udp : tcp, carrierPort(r, i), t('{name} (حامل)', { name: who })));
    const bad = msgs.find(Boolean);
    if (bad) return bad;
  }
  return '';
}

// When every location runs the same service ports (the same xray inbounds),
// give the other locations their own ports on the Iran server that lead to
// those same ports there: "37826:37825" is Iran's 37826 to that server's 37825.
function suggestPorts() {
  const src = rows.value.find((r) => r.node && (r.ports.trim() || r.udp.trim()));
  if (!src) { toast(t('اول پورت‌های یکی از لوکیشن‌ها را وارد کنید'), 'err'); return; }
  const { tcp, udp } = claims();
  for (const r of rows.value) for (const p of localPorts(r.ports)) tcp.set(p, 1);
  for (const r of rows.value) for (const p of localPorts(r.udp)) udp.set(p, 1);
  const remap = (list, used) => remotePorts(list).map((p) => {
    let ext = p + 1;
    while (used.has(ext) && ext < 65535) ext++;
    used.set(ext, 1);
    return ext + ':' + p;
  }).join(', ');
  for (const r of rows.value) {
    if (r === src || !r.node || r.ports.trim() || r.udp.trim()) continue;
    r.ports = remap(src.ports, tcp);
    r.udp = remap(src.udp, udp);
  }
}

// what each location takes on the Iran server, for the port map
const portMap = computed(() => rows.value.filter((r) => r.node).map((r) => ({
  name: nodeName(byId(r.node)), ip: byId(r.node).ip, tcp: portPairs(r.ports), udp: portPairs(r.udp),
})));

async function submit() {
  const why = check();
  if (why) { toast(why, 'err'); return; }
  const list = rows.value.map((r, i) => ({ r, i })).filter(({ r }) => r.node);
  const links = list.map(({ r, i }) => ({
    a: f.iran, b: r.node, kind: f.kind, reverse: f.reverse, transport: f.transport, mode: f.mode,
    port: perRowPort.value ? carrierPort(r, i) : Number(f.port) || 0, preset: f.preset, carriers: Number(f.carriers) || 0, mux_version: Number(f.mux_version) || 1,
    ports: r.ports, udp_ports: r.udp,
  }));
  busy.value = true;
  let ids;
  try {
    ids = (await api('POST', '/api/fleet/links', { self_role: f.iran === 'self' ? 'iran' : '', links })).ids;
  } catch (e) { fail(e); busy.value = false; return; }
  // the kernel cables of one Iran server are built together, in one run
  jobs.value = (kernel.value
    ? [{ id: ids[0], label: t('تانل کرنلی') + ' · ' + iranName.value }]
    : ids.map((id, k) => ({ id, label: iranName.value + ' ' + fwd() + ' ' + nodeName(byId(list[k].r.node)) })))
    .map((j) => ({ ...j, state: 'run', out: '' }));
  let ok = true;
  for (const j of jobs.value) {
    try {
      const r = await api('POST', '/api/fleet/apply', { id: j.id });
      Object.assign(j, { state: 'ok', out: steps(r.results) });
    } catch (e) {
      ok = false;
      Object.assign(j, { state: 'bad', out: e.message + '\n\n' + steps(e.data && e.data.results) });
    }
  }
  busy.value = false;
  finished.value = true;
  toast(ok ? t('تانل‌ها روی همهٔ سرورها ساخته شدند') : t('بعضی تانل‌ها ساخته نشدند؛ خروجی را ببینید'), ok ? 'ok' : 'err');
  await load();
}
</script>

<template>
  <div class="mform">
    <div class="grid-2">
      <div class="card">
        <div class="card-head"><h3><AppIcon name="node" /> {{ $t('سرور ایران') }}</h3><span class="tag">HUB</span></div>
        <FormField :label="$t('کاربران به کدام سرور ایران وصل می‌شوند؟')">
          <select v-model="f.iran" :disabled="busy || finished">
            <option value="" disabled>{{ $t('انتخاب کنید') }}</option>
            <option v-for="c in iranChoices" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </FormField>
        <p v-if="!iranChoices.length" class="hint warn">{{ $t('هیچ سرور ایرانی آماده نیست؛ اول در صفحهٔ سرورها یک سرور ایران اضافه کنید.') }}</p>
        <CableFields :l="f" :with-ports="false" />
        <p class="hint">{{ $t('هر سرور خارج پورت‌های خودش را دارد؛ کاربر با پورتی که وصل می‌شود لوکیشن را انتخاب می‌کند.') }}</p>
        <div v-if="existing.length" class="mexist">
          <div class="section-title">{{ $t('تانل‌های فعلی این سرور ایران') }}</div>
          <div v-for="(x, k) in existing" :key="k" class="mexist-row"><b>{{ x.name }}</b><span dir="ltr">{{ [x.ports, x.udp && 'UDP ' + x.udp].filter(Boolean).join(' · ') || '—' }}</span></div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <h3><AppIcon name="net" /> {{ $t('سرورهای خارج') }}</h3>
          <button type="button" class="btn small" :disabled="busy || finished" @click="rows.push(blank())"><AppIcon name="plus" /> {{ $t('سرور دیگر') }}</button>
        </div>
        <p v-if="!kharejs.length" class="hint warn">{{ $t('هنوز سرور خارجی اضافه نشده است.') }}</p>
        <div v-for="(r, i) in rows" :key="i" class="mpeer">
          <div class="mpeer-head"><b>{{ r.node ? nodeName(byId(r.node)) : $t('لوکیشن {n}', { n: fa(i + 1) }) }}</b>
            <button v-if="rows.length > 1" type="button" class="btn small danger icon-only" :title="$t('حذف')" :disabled="busy || finished" @click="rows.splice(i, 1)"><AppIcon name="trash" /></button></div>
          <FormField :label="$t('سرور خارج')">
            <select v-model="r.node" :disabled="busy || finished">
              <option value="" disabled>{{ $t('انتخاب کنید') }}</option>
              <option v-for="n in kharejs" :key="n.id" :value="n.id" :disabled="takenElsewhere(n.id, i) || (!finished && hasCable(n.id))">
                {{ nodeName(n) }}{{ n.ip ? ' · ' + n.ip : '' }}{{ !finished && hasCable(n.id) ? ' — ' + $t('از قبل کابل دارد') : '' }}
              </option>
            </select>
          </FormField>
          <div class="row">
            <FormField :label="$t('پورت‌های TCP این لوکیشن')"><input v-model="r.ports" type="text" dir="ltr" placeholder="37825, 62623" :disabled="busy || finished" /></FormField>
            <FormField :label="$t('پورت‌های UDP این لوکیشن')"><input v-model="r.udp" type="text" dir="ltr" placeholder="51820" :disabled="busy || finished" /></FormField>
            <FormField v-if="perRowPort" :label="$t('پورت حامل روی ایران')"><input v-model.number="r.port" type="number" dir="ltr" min="1" max="65535" :placeholder="String(carrierPort({}, i))" :disabled="busy || finished" /></FormField>
          </div>
        </div>
        <p class="hint">{{ $t('اگر سرویس روی همهٔ سرورهای خارج همان پورت‌ها را دارد، 37826:37825 یعنی پورت 37826 ایران به پورت 37825 آن سرور.') }}</p>
        <div class="mpeer-actions">
          <button type="button" class="btn small" :disabled="busy || finished || rows.length < 2" @click="suggestPorts"><AppIcon name="redo" /> {{ $t('پورت جدا برای بقیهٔ لوکیشن‌ها') }}</button>
          <button type="button" class="btn small" :disabled="busy || finished" @click="adding = true"><AppIcon name="plus" /> {{ $t('افزودن سرور خارج تازه') }}</button>
        </div>
      </div>
    </div>

    <div v-if="portMap.length" class="card mmap">
      <div class="card-head"><h3><AppIcon name="link" /> {{ $t('نقشهٔ پورت‌ها') }}</h3><span class="tag">PORT MAP</span></div>
      <table class="mmap-table">
        <thead><tr><th>{{ $t('لوکیشن') }}</th><th>{{ $t('پورت روی ایران') }}</th><th></th><th>{{ $t('پورت روی سرور خارج') }}</th></tr></thead>
        <tbody>
          <template v-for="m in portMap" :key="m.name">
            <tr v-for="(p, k) in m.tcp.concat(m.udp.map((x) => ({ ...x, udp: true })))" :key="m.name + k">
              <td v-if="k === 0" :rowspan="m.tcp.length + m.udp.length"><b>{{ m.name }}</b><div class="faint" dir="ltr">{{ m.ip }}</div></td>
              <td dir="ltr">{{ p.iran }}{{ p.udp ? ' UDP' : '' }}</td><td>{{ fwd() }}</td><td dir="ltr">{{ p.remote }}</td>
            </tr>
            <tr v-if="!m.tcp.length && !m.udp.length"><td><b>{{ m.name }}</b></td><td colspan="3" class="faint">{{ $t('هنوز پورتی وارد نشده') }}</td></tr>
          </template>
        </tbody>
      </table>
    </div>

    <div v-if="jobs.length" class="card mjobs">
      <div v-for="j in jobs" :key="j.id" class="mjob">
        <div class="mjob-head"><span class="mjob-st" :class="j.state">{{ STEP_ICON[j.state] }}</span><b>{{ j.label }}</b>
          <span class="faint">{{ j.state === 'run' ? $t('در حال ساخت…') : (j.state === 'ok' ? $t('انجام شد') : $t('خطا')) }}</span></div>
        <pre v-if="j.out" class="logs">{{ j.out }}</pre>
      </div>
      <p v-if="finished && jobs.some((j) => j.state === 'bad')" class="hint warn">{{ $t('کابل‌های ناموفق در طراحی شبکه مانده‌اند؛ از آنجا درستشان کنید و دوباره اعمال کنید.') }}</p>
    </div>

    <div class="form-actions">
      <button v-if="!finished" type="button" class="btn primary" :disabled="busy" @click="submit">
        <span v-if="busy" class="spin"></span><AppIcon v-else name="play" /> {{ $t('ساخت تانل‌ها روی همهٔ سرورها') }}</button>
      <button v-else type="button" class="btn primary" @click="emit('done', '', 'multi')"><AppIcon name="tunnel" /> {{ $t('مشاهدهٔ تانل‌ها') }}</button>
      <button type="button" class="btn" :disabled="busy" @click="emit('cancel')">{{ $t(finished ? 'بستن' : 'انصراف') }}</button>
    </div>

    <AddServerWindow v-if="adding" @close="adding = false; load()" @added="load()" />
  </div>
</template>

<style>
.mform .grid-2 { margin-top: 14px; }
.mpeer { padding: 12px 0; border-top: 1px dashed var(--line-2); }
.mpeer:first-of-type { border-top: 0; padding-top: 0; }
.mpeer-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.mpeer-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.mexist { margin-top: 12px; }
.mexist-row { display: flex; justify-content: space-between; gap: 10px; padding: 4px 0; font-size: 13px; }
.mmap { margin-top: 14px; overflow-x: auto; }
.mmap-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.mmap-table th { text-align: start; font-weight: 500; color: var(--ti-3); padding: 6px 8px; }
.mmap-table td { padding: 6px 8px; border-top: 1px solid var(--line-2); vertical-align: top; }
.mjobs { margin-top: 14px; }
.mjob + .mjob { margin-top: 12px; }
.mjob-head { display: flex; align-items: center; gap: 8px; }
.mjob-st.ok { color: var(--ok); }
.mjob-st.bad { color: var(--bad); }
.mjob .logs { margin-top: 8px; max-height: 220px; }
</style>
