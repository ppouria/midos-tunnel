<script setup>
import { ref, reactive, computed, shallowRef, toRaw, watch, onMounted, onBeforeUnmount, nextTick } from 'vue';
import { api } from '../lib/api.js';
import { store } from '../lib/store.js';
import { usePoll } from '../lib/poll.js';
import { fail, toast } from '../lib/toast.js';
import { confirmBox } from '../lib/confirm.js';
import { split, bytes, fa } from '../lib/format.js';
import { t, i18n, pct } from '../lib/i18n.js';
import { TRANSPORTS, PRESETS, MUX_VERSIONS, MUX_HELP } from '../lib/constants.js';
import { NODE_STATE, roleOf, nodeName, cableState, cableRtt, localPorts } from '../lib/fleet.js';
import { createEngine, KIND, CABLE, KERNEL_MODES, snap, cableType } from '../topology/engine.js';
import { DEF_PORT, STATE_TEXT, uid, serialize, cableLabel, benchSummary } from '../topology/model.js';
import PageHead from '../components/PageHead.vue';
import SegControl from '../components/SegControl.vue';
import FormField from '../components/FormField.vue';
import GlassWindow from '../components/GlassWindow.vue';
import AppIcon from '../components/AppIcon.vue';
import AddServerWindow from '../components/AddServerWindow.vue';
import CableEditWindow from '../components/CableEditWindow.vue';
import ReaddressWindow from '../components/ReaddressWindow.vue';
import BenchCard from '../topology/BenchCard.vue';
import '../styles/topology.css';

// The designer draws the fleet: the mother server, the Iran and Kharej servers
// it manages and the cables between any Iran and any Kharej server. Adding a
// server, building a cable and removing either all happen on the mother, which
// reaches every server with its own SSH key.
const topo = reactive({ self_role: 'none', nodes: [], links: [] });
const selfIP = ref('');
const selected = ref(null);
const tool = ref('select');
const cableKind = ref('direct');
const fleet = shallowRef({}); // node id -> live view from /api/fleet
const bench = shallowRef({ running: {}, results: {} });
const saveTag = ref('NETWORK DESIGNER // 3D');
const canvas = ref(null);
const dropping = ref(false);
const loaded = ref(false);
const adding = ref(null); // { node, watch? } while the add window is open
const editCable = ref(''); // the applied cable whose edit window is open
const moving = ref(null); // the server whose IP is being changed
let engine = null;

const raw = () => toRaw(topo);
const nodeById = (id) => topo.nodes.find((n) => n.id === id);
const linkById = (id) => topo.links.find((l) => l.id === id);
const selfNode = () => topo.nodes.find((n) => n.kind === 'self');
const role = (n) => roleOf(n, topo.self_role);
const isReady = (n) => n && (n.kind === 'self' || n.state === 'ready');
const redraw = () => engine && engine.redraw();

// ------------------------------------------------------------- saving
let saveTimer = 0;
async function saveNow() {
  clearTimeout(saveTimer);
  saveTimer = 0;
  try { await api('PUT', '/api/topology', serialize(raw())); saveTag.value = 'ذخیره شد ✓'; }
  catch (e) { saveTag.value = 'ذخیره نشد'; fail(e); throw e; }
}
function save() {
  clearTimeout(saveTimer);
  saveTag.value = 'در حال ذخیره…';
  saveTimer = setTimeout(() => saveNow().catch(() => {}), 500);
}
function changed() { redraw(); save(); }

// ------------------------------------------------------------- live state
const liveById = () => {
  const m = { ...fleet.value };
  for (const n of topo.nodes) if (!m[n.id]) m[n.id] = { ...n, role: role(n) };
  return m;
};
const CABLE_KEYS = ['kind', 'transport', 'port', 'ports', 'udp_ports', 'reverse', 'preset', 'carriers', 'mux_version', 'mode', 'tunnel'];
const linkState = (l) => cableState(l, liveById());
const linkRtt = (l) => cableRtt(l, liveById());

async function refresh() {
  const [f, b] = await Promise.all([api('GET', '/api/fleet'), api('GET', '/api/topology/bench').catch(() => null)]);
  const m = {};
  for (const n of f.nodes) m[n.id] = n;
  fleet.value = m;
  // what only the mother sets comes from it; the layout stays the page's
  for (const n of topo.nodes) {
    const v = m[n.id];
    if (!v || n.kind === 'self') continue;
    Object.assign(n, { state: v.state || '', error: v.error || '', facts: v.facts || null, host_key: v.host_key || '' });
    if (v.state) Object.assign(n, { ip: v.ip, ssh_user: v.ssh_user, ssh_port: v.ssh_port });
  }
  for (const n of f.nodes) {
    // a server added from the servers page appears on the floor too
    if (n.kind !== 'self' && !nodeById(n.id)) topo.nodes.push({ id: n.id, kind: n.kind, label: n.label, ip: n.ip, x: n.x, y: n.y, ssh_user: n.ssh_user, ssh_port: n.ssh_port, state: n.state, facts: n.facts, host_key: n.host_key });
  }
  topo.nodes = topo.nodes.filter((n) => n.kind === 'self' || !n.state || m[n.id]);
  const fl = {};
  for (const l of f.links) fl[l.id] = l;
  topo.links = topo.links.filter((l) => !l.tunnel || fl[l.id]);
  // an applied cable changes only on the mother (edit, unlink), so its
  // settings come from there; one built from the tunnel form appears here
  for (const v of f.links) {
    const l = linkById(v.id);
    if (!l) { if (v.tunnel) topo.links.push({ ...v }); continue; }
    if (v.tunnel) for (const k of CABLE_KEYS) l[k] = v[k];
    else l.tunnel = '';
  }
  if (b) bench.value = { running: b.running || {}, results: b.results || {} };
  redraw();
}
usePoll(() => (loaded.value ? refresh() : null), 4000);

// ------------------------------------------------------------- editing
function setTool(t, kind) {
  tool.value = t;
  if (kind) cableKind.value = kind;
  if (engine) engine.cancelCable();
}
function select(s) { selected.value = s; }

function addNode(kind, x, y) {
  const count = topo.nodes.filter((n) => n.kind === kind).length + 1;
  const n = { id: uid('n'), kind, label: (kind === 'iran' ? 'iran-' : 'kharej-') + count, ip: '', x: snap(x), y: snap(y), ssh_user: 'root', ssh_port: 22 };
  topo.nodes.push(n);
  select({ type: 'node', id: n.id });
  changed();
}

function addLink(aId, bId) {
  const a = nodeById(aId), b = nodeById(bId);
  if (!a || !b || a.id === b.id) return;
  const ra = role(a), rb = role(b);
  if (ra === 'none' || rb === 'none') { toast(t('سرور مادر نقش «فقط مدیر» دارد؛ برای تانل‌کشی با خودش، نقشش را ایران یا خارج کنید'), 'err'); return; }
  if (ra === rb) { toast(t('دو سر کابل باید یکی ایران و دیگری خارج باشد'), 'err'); return; }
  if (topo.links.some((l) => (l.a === aId && l.b === bId) || (l.a === bId && l.b === aId))) { toast(t('این دو سرور از قبل کابل دارند'), 'err'); return; }
  const kh = ra === 'kharej' ? a : b;
  if (cableKind.value === 'kernel' && topo.links.some((l) => l.kind === 'kernel' && (l.a === kh.id || l.b === kh.id))) {
    toast(t('هر سرور خارج فقط با یک سرور ایران تانل کرنلی می‌سازد'), 'err');
    return;
  }
  // the Iran end first keeps cable labels reading Iran → Kharej
  const [ia, kb] = ra === 'iran' ? [aId, bId] : [bId, aId];
  const l = cableKind.value !== 'kernel'
    ? { id: uid('l'), a: ia, b: kb, kind: 'direct', transport: 'tls', port: 443, ports: '443', udp_ports: '', reverse: cableKind.value === 'reverse', preset: 'balance', carriers: 2 }
    : { id: uid('l'), a: ia, b: kb, kind: 'kernel', mode: 'esp', ports: '443', udp_ports: '' };
  topo.links.push(l);
  select({ type: 'link', id: l.id });
  changed();
}

const dropLink = (id) => { topo.links = topo.links.filter((l) => l.id !== id); };

async function removeNode(n) {
  if (!n || n.kind === 'self') return;
  if (n.state) {
    const own = topo.links.filter((l) => (l.a === n.id || l.b === n.id) && l.tunnel).length;
    const ok = await confirmBox(t('حذف سرور «{name}»', { name: nodeName(n) }),
      (own ? t('تانل‌های این سرور ({n}) از روی سرورهای درگیر برداشته می‌شوند.', { n: fa(own) }) + ' ' : '') + t('کلید سرور مادر هم از سرور پاک می‌شود.'), t('حذف'), true);
    if (!ok) return;
    try {
      await saveNow();
      await api('POST', '/api/fleet/remove', { id: n.id, purge: true });
      toast(t('سرور حذف شد'), 'ok');
    } catch (e) { fail(e); return; }
  }
  topo.nodes = topo.nodes.filter((x) => x.id !== n.id);
  topo.links = topo.links.filter((l) => l.a !== n.id && l.b !== n.id);
  select(null);
  changed();
}

async function removeLink(l) {
  if (!l) return;
  if (l.tunnel) {
    const ok = await confirmBox(t('حذف کابل'), l.kind === 'kernel'
      ? t('تانل کرنلی سرور خارج برداشته می‌شود و سرور ایران با بقیهٔ سرورهای خارجش دوباره ساخته می‌شود.')
      : t('تانل این کابل از روی هر دو سرور حذف می‌شود.'), t('حذف تانل'), true);
    if (!ok) return;
    Object.assign(jobs, { open: true, title: 'حذف کابل', rows: [], done: false });
    const job = jobRow(t(CABLE[cableType(l)].name) + ' · ' + cableEnds(l));
    try {
      await saveNow();
      const r = await api('POST', '/api/fleet/unlink', { id: l.id });
      job.done(true, 'تانل از روی سرورها برداشته شد.');
      for (const s of r.results || []) job.output(fmtStep(s));
    } catch (e) {
      job.done(false, e.message);
      for (const s of (e.data && e.data.results) || []) job.output(fmtStep(s));
    }
    jobs.done = true;
  }
  dropLink(l.id);
  select(null);
  changed();
}

function removeSelected() {
  const s = selected.value;
  if (!s) return;
  if (s.type === 'node') removeNode(nodeById(s.id));
  else removeLink(linkById(s.id));
}

function autoLayout() {
  const self = selfNode();
  if (self) { self.x = 0; self.y = 0; }
  // Iran servers on the right half, Kharej on the left, the mother between
  for (const [k, side] of [['iran', 1], ['kharej', -1]]) {
    const list = topo.nodes.filter((n) => n.kind === k);
    list.forEach((n, i) => {
      n.x = snap(side * 300);
      n.y = snap((i - (list.length - 1) / 2) * 200);
    });
  }
  engine.fit();
  save();
}

// ------------------------------------------------------------- inspector
const selNode = computed(() => (selected.value && selected.value.type === 'node' ? nodeById(selected.value.id) : null));
const selLink = computed(() => (selected.value && selected.value.type === 'link' ? linkById(selected.value.id) : null));
const selLive = computed(() => (selNode.value ? fleet.value[selNode.value.id] : null));
const cableEnds = (l) => nodeName(nodeById(l.a) || {}) + ' ↔ ' + nodeName(nodeById(l.b) || {});
const inspectTitle = computed(() => {
  if (selLink.value) return t('کابل {ends}', { ends: cableEnds(selLink.value) });
  return t('تنظیمات {name}', { name: selNode.value ? selNode.value.label || t(KIND[selNode.value.kind].name) : '' });
});
const linkSt = computed(() => (selLink.value ? linkState(selLink.value) : 'draft'));
const linkNotReady = computed(() => (selLink.value ? [nodeById(selLink.value.a), nodeById(selLink.value.b)].filter((n) => !isReady(n)) : []));
const nodeLinks = computed(() => (selNode.value ? topo.links.filter((l) => l.a === selNode.value.id || l.b === selNode.value.id) : []));
const specRows = computed(() => {
  const f = (selNode.value && (selNode.value.kind === 'self' ? (selLive.value || {}).facts : selNode.value.facts)) || {};
  return [[t('سیستم‌عامل'), f.os], [t('پردازنده'), f.cpu ? t('{cpu} · {n} هسته', { cpu: f.cpu, n: fa(f.cores) }) : ''], [t('حافظه'), f.mem_total ? bytes(f.mem_total) : ''],
    [t('دیسک'), f.disk_total ? bytes(f.disk_total) : ''], ['midos-tunnel', f.veil]].filter(([, v]) => v);
});
// the designer configures every transport but ssh, which needs a login field it does not have
const TRANSPORT_OPTS = Object.entries(TRANSPORTS).filter(([k]) => k !== 'ssh');

function setSelfRole(v) {
  const s = selfNode();
  if (s && topo.links.some((l) => l.a === s.id || l.b === s.id)) { toast(t('سرور مادر کابل دارد؛ اول کابل‌هایش را حذف کنید'), 'err'); return; }
  topo.self_role = v; changed();
}
function setNodeKind(v) {
  const n = selNode.value;
  if (topo.links.some((l) => l.a === n.id || l.b === n.id)) { toast(t('این سرور کابل دارد؛ اول کابل‌هایش را حذف کنید'), 'err'); return; }
  n.kind = v; changed();
}
function setLinkKind(v) {
  const l = selLink.value;
  if (l.tunnel) { editCable.value = l.id; return; }
  const kh = role(nodeById(l.a)) === 'kharej' ? l.a : l.b;
  if (v === 'kernel' && topo.links.some((x) => x.kind === 'kernel' && x.id !== l.id && (x.a === kh || x.b === kh))) { toast(t('این سرور خارج کابل کرنلی دیگری دارد'), 'err'); return; }
  l.kind = v === 'kernel' ? 'kernel' : 'direct';
  l.reverse = v === 'reverse';
  if (v === 'kernel') l.mode = l.mode || 'esp';
  else { l.transport = l.transport || 'tls'; l.port = l.port || 443; }
  changed();
}
function setTransport() {
  const l = selLink.value;
  if (l.transport !== 'icmp' && DEF_PORT[l.transport]) l.port = DEF_PORT[l.transport];
  if (l.transport === 'icmp') l.carriers = 1;
  changed();
}

async function openAdd(n, watchIt) {
  try { await saveNow(); } catch (_) { return; } // the mother must know the device first
  adding.value = { node: { ...toRaw(n) }, watch: watchIt ? n.id : '' };
}

// ------------------------------------------------------------- applying
const jobs = reactive({ open: false, title: '', rows: [], done: false });
function jobRow(label) {
  const row = reactive({ label, state: 'run', notes: [], outputs: [] });
  jobs.rows.push(row);
  return {
    done(ok, text) { row.state = ok ? 'ok' : 'bad'; if (text) row.notes.push({ ok, text }); },
    output(text) { if (text) row.outputs.push(text); },
  };
}
const fmtStep = (s) => s.step + (s.ok ? ' ✓' : ' ✗') + '\n' + (s.error ? s.error + '\n' : '') + (s.output || '').split('\n').slice(-12).join('\n');

function checkReady(l) {
  const a = nodeById(l.a), b = nodeById(l.b);
  if (!a || !b) return t('یک سر کابل پیدا نشد');
  if (role(a) === role(b) || role(a) === 'none' || role(b) === 'none') return t('دو سر کابل {ends} باید یکی ایران و دیگری خارج باشد', { ends: cableEnds(l) });
  for (const n of [a, b]) if (!isReady(n)) return t('سرور «{name}» هنوز اضافه و نصب نشده است', { name: nodeName(n) });
  if (!split(l.ports).length && !split(l.udp_ports).length) return t('برای کابل {ends} حداقل یک پورت وارد کنید', { ends: cableEnds(l) });
  return portClash(l);
}

// Two cables of one Iran server forwarding the same port there: only one of
// them would ever get the traffic, so each location needs ports of its own.
const iranEnd = (l) => (role(nodeById(l.a)) === 'iran' ? l.a : l.b);
function portClash(l) {
  const hub = iranEnd(l);
  const far = (x) => nodeName(nodeById(x.a === hub ? x.b : x.a) || {});
  for (const x of topo.links) {
    if (x.id === l.id || iranEnd(x) !== hub) continue;
    for (const [mine, theirs] of [[l.ports, x.ports], [l.udp_ports, x.udp_ports]]) {
      const taken = new Set(localPorts(theirs));
      const p = localPorts(mine).find((q) => taken.has(q));
      if (p) return t('پورت {p} روی سرور ایران هم برای «{a}» است هم «{b}»', { p, a: far(l), b: far(x) });
    }
  }
  return '';
}

async function runJobs(links) {
  if (!links.length) { toast(t('کابل اعمال‌نشده‌ای نیست'), 'ok'); return; }
  for (const l of links) { const why = checkReady(l); if (why) { toast(why, 'err'); select({ type: 'link', id: l.id }); return; } }
  // kernel cables of one Iran server are built together
  const hubs = new Map();
  for (const l of links.filter((x) => x.kind === 'kernel')) {
    const hub = role(nodeById(l.a)) === 'iran' ? l.a : l.b;
    if (!hubs.has(hub)) hubs.set(hub, l);
  }
  if (hubs.size) {
    const ok = await confirmBox(t('تانل کرنلی'), t('تانل کرنلی هر سرور ایران با همهٔ کابل‌های کرنلی‌اش (یک پیکربندی مشترک) ساخته یا بازسازی می‌شود. ادامه؟'), t('ادامه'));
    if (!ok) return;
  }
  try { await saveNow(); } catch (_) { return; }
  Object.assign(jobs, { open: true, title: 'اعمال کابل‌ها', rows: [], done: false });
  const todo = links.filter((x) => x.kind === 'direct').concat([...hubs.values()]);
  for (const l of todo) {
    const label = l.kind === 'kernel'
      ? t(CABLE.kernel.name) + ' · ' + t('{name} (هاب)', { name: nodeName(nodeById(role(nodeById(l.a)) === 'iran' ? l.a : l.b)) })
      : t(CABLE[cableType(l)].name) + ' · ' + cableEnds(l);
    const job = jobRow(label);
    try {
      const r = await api('POST', '/api/fleet/apply', { id: l.id });
      job.done(true, l.kind === 'kernel' ? 'تانل کرنلی روی سرور ایران و سرورهای خارجش ساخته شد.' : 'تانل روی هر دو سرور نصب و روشن شد.');
      for (const s of r.results || []) job.output(fmtStep(s));
    } catch (e) {
      job.done(false, e.message);
      for (const s of (e.data && e.data.results) || []) job.output(fmtStep(s));
    }
  }
  jobs.done = true;
  try { await refresh(); } catch (_) { /* the poll will catch up */ }
}

function applyAll() {
  const pending = topo.links.filter((l) => !l.tunnel);
  runJobs(pending);
}

// ------------------------------------------------------------- benchmark
async function startBench(n) {
  if (!isReady(n)) { toast(t('اول سرور را اضافه و نصب کنید'), 'err'); return; }
  try {
    await api('POST', '/api/topology/bench', { id: n.id });
    bench.value = { ...bench.value, running: { ...bench.value.running, [n.id]: { tail: '', started: Date.now() / 1000 } } };
    toast(t('بنچمارک شروع شد؛ چند دقیقه طول می‌کشد'), 'ok');
    redraw();
  } catch (e) { fail(e); }
}
function deviceNote(n) {
  if (n.kind !== 'self') {
    if (n.state === 'installing') return '⏳ ' + t('در حال نصب…');
    if (n.state === 'error') return '⚠ ' + t('نصب ناموفق');
    if (!n.state) return t('اضافه نشده');
    const live = fleet.value[n.id];
    if (live && live.stats && live.stats.error) return '⚠ ' + t('آفلاین');
  }
  return benchSummary(bench.value, n);
}

// ------------------------------------------------------------- drag & drop
function onDragStart(e, kind) { e.dataTransfer.setData('text/plain', 'device:' + kind); e.dataTransfer.effectAllowed = 'copy'; }
function onDrop(e) {
  dropping.value = false;
  const data = e.dataTransfer.getData('text/plain') || '';
  if (!data.startsWith('device:')) return;
  const kind = data.slice(7);
  if (kind !== 'iran' && kind !== 'kharej') return;
  const r = canvas.value.getBoundingClientRect();
  const w = engine.toWorld(e.clientX - r.left, e.clientY - r.top);
  addNode(kind, w.x, w.y);
}
function addAtCenter(kind) { const c = engine.center(); addNode(kind, c.x + 120, c.y + 60); }

function onKey(e) {
  if (/^(INPUT|TEXTAREA|SELECT)$/.test((document.activeElement || {}).tagName || '')) return;
  if (document.querySelector('.win-backdrop')) return; // a modal window is open: its keys win
  if (e.key === 'Escape') setTool('select');
  if ((e.key === 'Delete' || e.key === 'Backspace') && selected.value) { e.preventDefault(); removeSelected(); }
}

watch([selected, tool, cableKind], redraw);
watch(() => i18n.lang, redraw);

onMounted(async () => {
  try {
    const d = await api('GET', '/api/topology');
    Object.assign(topo, d.topology || {});
    selfIP.value = d.self_ip || '';
  } catch (e) { fail(e); }
  if (!selfNode()) topo.nodes.unshift({ id: 'self', kind: 'self', label: (store.session && store.session.hostname) || t('سرور مادر'), ip: '', x: 0, y: 0 });
  await nextTick();
  engine = createEngine(canvas.value, {
    topo: raw, selected: () => selected.value, tool: () => tool.value, cableKind: () => cableKind.value, selfIP: () => selfIP.value,
    linkState, linkLabel: (l) => cableLabel(l, linkRtt(l)), benchSummary: deviceNote,
    select, addLink, moved: save,
  });
  requestAnimationFrame(() => engine.fit());
  loaded.value = true;
  refresh().catch(() => {});
  document.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey);
  if (saveTimer) saveNow().catch(() => {}); // do not lose the last edit
  if (engine) engine.destroy();
});
</script>

<template>
  <div>
    <PageHead :title="$t('طراحی شبکه')" :tag="$t(saveTag)" />
    <div class="tp-toolbar">
      <button class="btn small" :class="{ on: tool === 'select' }" :title="$t('انتخاب و جابه‌جایی')" @click="setTool('select')"><AppIcon name="chev" /> {{ $t('انتخاب') }}</button>
      <button class="btn small" :title="$t('چرخش نما ۹۰ درجه')" @click="engine.rotate()"><AppIcon name="rotate" /> {{ $t('چرخش') }}</button>
      <button class="btn small icon-only" :title="$t('بزرگ‌نمایی')" @click="engine.zoomAt(1.2)"><AppIcon name="zoomin" /></button>
      <button class="btn small icon-only" :title="$t('کوچک‌نمایی')" @click="engine.zoomAt(1 / 1.2)"><AppIcon name="zoomout" /></button>
      <button class="btn small" :title="$t('نمایش همه')" @click="engine.fit()"><AppIcon name="fit" /> {{ $t('نمایش همه') }}</button>
      <button class="btn small" :title="$t('چیدمان خودکار دور این سرور')" @click="autoLayout"><AppIcon name="node" /> {{ $t('چیدمان خودکار') }}</button>
      <span class="tp-spacer"></span>
      <button class="btn primary" @click="applyAll"><AppIcon name="play" /> {{ $t('اعمال همهٔ کابل‌ها') }}</button>
    </div>

    <div class="tp-wrap">
      <aside class="tp-palette card">
        <div class="tag">DEVICES</div>
        <div v-for="k in ['iran', 'kharej']" :key="k" class="tp-pal-item" draggable="true" :title="$t('بکشید و رها کنید')" @dragstart="onDragStart($event, k)" @click="addAtCenter(k)">
          <svg viewBox="0 0 40 40" class="tp-pal-icon">
            <polygon points="20,6 34,13 20,20 6,13" fill="#2b333b" :stroke="KIND[k].color" stroke-width="1" />
            <polygon points="6,13 20,20 20,34 6,27" fill="#1a2026" />
            <polygon points="20,20 34,13 34,27 20,34" fill="#11151a" />
            <circle v-for="y in [18, 22, 26]" :key="y" cx="9" :cy="y" r="1.3" :fill="KIND[k].color" class="tp-led" />
            <line x1="20" y1="20" x2="20" y2="34" :stroke="KIND[k].color" stroke-width="1.2" />
          </svg>
          <div><b>{{ $t(KIND[k].name) }}</b><small>{{ k === 'iran' ? 'IRAN NODE' : 'KHAREJ NODE' }}</small></div>
        </div>
        <div class="tag">CABLES</div>
        <div v-for="k in ['direct', 'reverse', 'kernel']" :key="k" class="tp-pal-item cable" :class="{ on: tool === 'cable' && cableKind === k }"
          :title="$t('روی سرور اول و بعد سرور دوم کلیک کنید')" @click="setTool(tool === 'cable' && cableKind === k ? 'select' : 'cable', k)">
          <svg viewBox="0 0 40 40" class="tp-pal-icon">
            <path pathLength="1" d="M6 30 C 14 4, 26 4, 34 30" fill="none" :stroke="CABLE[k].color" stroke-width="3" stroke-linecap="round" class="tp-pal-cable" />
            <circle cx="6" cy="30" r="3" :fill="CABLE[k].color" /><circle cx="34" cy="30" r="3" :fill="CABLE[k].color" />
          </svg>
          <div><b>{{ $t(CABLE[k].name) }}</b><small>{{ { direct: 'IRAN → KHAREJ', reverse: 'KHAREJ → IRAN', kernel: 'ESP / MOI' }[k] }}</small></div>
        </div>
        <div class="tp-legend">
          <div><i class="lg up"></i>{{ $t('متصل') }}</div><div><i class="lg warn"></i>{{ $t('بدون حامل') }}</div>
          <div><i class="lg down"></i>{{ $t('خطا') }}</div><div><i class="lg draft"></i>{{ $t('اعمال‌نشده') }}</div>
        </div>
      </aside>

      <div class="tp-stage" :class="{ cabling: tool === 'cable', drop: dropping }"
        @dragover.prevent="dropping = true" @dragleave="dropping = false" @drop.prevent="onDrop">
        <svg ref="canvas" class="tp-canvas"></svg>
        <div v-if="loaded && topo.nodes.length < 2" class="tp-hint"><AppIcon name="node" /><span>{{ $t('سرورهای ایران و خارج را از پالت بکشید و روی صفحه رها کنید، بعد با ابزار کابل آن‌ها را به هم وصل کنید.') }}</span></div>
        <div v-if="tool === 'cable'" class="tp-mode"><AppIcon name="link" /> {{ $t('{cable}: روی سرور اول و بعد سرور دوم کلیک کنید · Esc برای لغو', { cable: $t(CABLE[cableKind].name) }) }}</div>
      </div>

      <aside class="tp-inspector card">
        <div class="card-head"><h3><AppIcon name="node" /> {{ $t('راهنما') }}</h3></div>
        <ol class="tp-steps">
          <li>{{ $t('سرور ایران یا خارج را از پالت بکشید، IP بدهید و «افزودن و نصب خودکار» را بزنید.') }}</li>
          <li>{{ $t('سرور مادر سیستم را به‌روز، پیش‌نیازها را نصب و سرور را بنچمارک می‌کند.') }}</li>
          <li>{{ $t('با ابزار کابل (مستقیم، معکوس یا کرنلی) هر سرور ایران را به هر سرور خارج وصل کنید؛ دو به دو یا یک به چند. در «معکوس» سرور خارج به ایران وصل می‌شود.') }}</li>
          <li>{{ $t('«اعمال» را بزنید؛ تانل روی هر دو سرور ساخته و روی کارت‌هایشان نمایش داده می‌شود.') }}</li>
        </ol>
        <p class="muted">{{ $t('روی هر سرور یا کابل کلیک کنید تا پنجرهٔ تنظیماتش باز شود.') }}</p>
        <p class="muted">{{ $t('چرخ ماوس: بزرگ‌نمایی · کشیدن پس‌زمینه: جابه‌جایی نما · Delete: حذف · Esc: لغو') }}</p>
      </aside>
    </div>

    <GlassWindow v-if="selNode || selLink" :title="inspectTitle" :icon="selLink ? 'link' : 'cpu'" :width="380" :modal="false" place="side" @close="selected = null">
      <div class="tp-insp">
      <!-- this server -->
      <template v-if="selNode && selNode.kind === 'self'">
        <div class="card-head"><h3><AppIcon name="shield" /> {{ $t('سرور مادر') }}</h3><span class="pill accent">MOTHER</span></div>
        <p class="muted">{{ $t('پنل روی این سرور است و همهٔ سرورهای ایران و خارج را از همین‌جا اضافه، نصب و تانل می‌کند.') }}</p>
        <FormField :label="$t('نام')"><input v-model="selNode.label" type="text" maxlength="40" @input="changed" /></FormField>
        <FormField :label="$t('نقش خود سرور مادر')" :hint="$t('«فقط مدیر» یعنی خودش در هیچ تانلی شرکت نمی‌کند و فقط سرورهای دیگر را مدیریت می‌کند.')">
          <SegControl :model-value="topo.self_role" :options="[['none', $t('فقط مدیر')], ['iran', $t('ایران')], ['kharej', $t('خارج')]]" @update:model-value="setSelfRole" />
        </FormField>
        <FormField :label="$t('IP عمومی')"><input :value="selfIP" type="text" dir="ltr" disabled /></FormField>
        <dl v-if="specRows.length" class="srv-specs"><template v-for="[k, v] in specRows" :key="k"><dt>{{ k }}</dt><dd>{{ v }}</dd></template></dl>
        <BenchCard :node="selNode" :bench="bench" @run="startBench(selNode)" />
      </template>

      <!-- another server -->
      <template v-else-if="selNode">
        <div class="card-head"><h3><AppIcon name="cpu" /> {{ $t(KIND[selNode.kind].name) }}</h3>
          <span class="pill" :class="(NODE_STATE[selNode.state || ''] || [])[0]">{{ $t((NODE_STATE[selNode.state || ''] || [])[1]) }}</span></div>
        <FormField :label="$t('نام')"><input v-model="selNode.label" type="text" maxlength="40" @input="changed" /></FormField>
        <FormField :label="$t('نوع')"><SegControl :model-value="selNode.kind" :options="[['iran', $t('ایران')], ['kharej', $t('خارج')]]" @update:model-value="setNodeKind" /></FormField>
        <FormField :label="$t('IP عمومی')" :hint="selNode.state ? $t('برای IP تازه از دکمهٔ «تغییر IP» استفاده کنید؛ تانل‌ها هم دوباره ساخته می‌شوند.') : ''">
          <input v-model.trim="selNode.ip" type="text" dir="ltr" placeholder="203.0.113.10" :disabled="!!selNode.state" @input="changed" />
        </FormField>
        <button v-if="selNode.state === 'ready'" class="btn small" @click="moving = { ...selNode }"><AppIcon name="net" /> {{ $t('تغییر IP') }}</button>

        <template v-if="!selNode.state || selNode.state === 'error'">
          <div v-if="selNode.state === 'error'" class="hint warn">{{ selNode.error }}</div>
          <p v-else class="muted">{{ $t('این سرور هنوز فقط روی نقشه است. با «افزودن و نصب» سرور مادر وارد آن می‌شود، سیستم را به‌روز و همهٔ پیش‌نیازهای تانل را خودکار نصب می‌کند.') }}</p>
          <div class="actions">
            <button class="btn primary" :disabled="!selNode.ip" @click="openAdd(selNode)"><AppIcon name="plus" /> {{ selNode.state ? $t('تلاش دوباره') : $t('افزودن و نصب خودکار') }}</button>
            <button v-if="selLive && selLive.job" class="btn small" @click="openAdd(selNode, true)"><AppIcon name="list" /> {{ $t('خروجی نصب') }}</button>
          </div>
        </template>
        <template v-else-if="selNode.state === 'installing'">
          <div class="hint warn"><span class="spin"></span> {{ $t('در حال نصب: {step}', { step: $t(((selLive && selLive.job && selLive.job.steps.find((s) => s.state === 'run')) || {}).name || '…') }) }}</div>
          <button class="btn small" @click="openAdd(selNode, true)"><AppIcon name="list" /> {{ $t('مشاهدهٔ مراحل') }}</button>
        </template>
        <template v-else>
          <dl v-if="specRows.length" class="srv-specs"><template v-for="[k, v] in specRows" :key="k"><dt>{{ k }}</dt><dd>{{ v }}</dd></template></dl>
          <div v-if="selLive && selLive.stats && selLive.stats.error" class="hint warn">{{ selLive.stats.error }}</div>
          <div v-else-if="selLive && selLive.stats" class="tp-live">
            <span>CPU <b class="num">{{ fa(Math.round(selLive.stats.cpu)) }}{{ pct() }}</b></span>
            <span>RAM <b class="num">{{ fa(Math.round(100 * selLive.stats.mem_used / (selLive.stats.mem_total || 1))) }}{{ pct() }}</b></span>
            <span>DISK <b class="num">{{ fa(Math.round(100 * selLive.stats.disk_used / (selLive.stats.disk_total || 1))) }}{{ pct() }}</b></span>
          </div>
          <BenchCard :node="selNode" :bench="bench" @run="startBench(selNode)" />
          <div class="actions">
            <router-link to="/servers" class="btn small"><AppIcon name="cpu" /> {{ $t('کارت سرور') }}</router-link>
            <button class="btn small" @click="openAdd(selNode)"><AppIcon name="redo" /> {{ $t('نصب دوباره') }}</button>
          </div>
        </template>
        <div class="tp-node-links faint">{{ nodeLinks.length ? $t('تانل با: {list}', { list: nodeLinks.map((l) => nodeName(nodeById(l.a === selNode.id ? l.b : l.a) || {})).join($t('، ')) }) : $t('هنوز کابلی ندارد') }}</div>
        <button class="btn danger small" @click="removeSelected"><AppIcon name="trash" /> {{ $t('حذف سرور') }}</button>
      </template>

      <!-- a cable -->
      <template v-else-if="selLink">
        <div class="card-head"><h3><AppIcon name="link" /> {{ $t('کابل') }}</h3><span class="pill tp-st" :class="'st-' + linkSt">{{ $t(STATE_TEXT[linkSt]) }}</span></div>
        <div class="tp-route"><span>{{ (nodeById(selLink.a) || {}).label || '?' }}</span><AppIcon name="link" /><span>{{ (nodeById(selLink.b) || {}).label || '?' }}</span></div>
        <FormField :label="$t('نوع کابل')"><SegControl :model-value="cableType(selLink)" :options="[['direct', $t('مستقیم')], ['reverse', $t('معکوس')], ['kernel', $t('کرنلی')]]" @update:model-value="setLinkKind" /></FormField>
        <p v-if="selLink.kind === 'direct'" class="muted tp-dir">{{ selLink.reverse
          ? $t('معکوس: سرور خارج به سرور ایران وصل می‌شود؛ پورت حامل روی ایران باز می‌شود و روی خارج پورت ورودی لازم نیست. برای خارجی که پشت فایروال است یا IP ورودی‌اش بسته است.')
          : $t('مستقیم: سرور ایران به سرور خارج وصل می‌شود؛ پورت حامل روی خارج باز می‌شود.') }}</p>
        <div v-if="selLink.tunnel" class="tp-applied"><AppIcon name="shield" /><span>{{ selLink.kind === 'kernel' ? $t('تانل کرنلی اعمال شده') : $t('تانل «{name}»', { name: selLink.tunnel }) }}</span></div>

        <template v-if="selLink.kind === 'direct'">
          <FormField :label="$t('ترنسپورت')" :hint="$t((TRANSPORTS[selLink.transport] || {}).help)">
            <select v-model="selLink.transport" :disabled="!!selLink.tunnel" @change="setTransport">
              <option v-for="[k, v] in TRANSPORT_OPTS" :key="k" :value="k">{{ v.name }}</option>
            </select>
          </FormField>
          <FormField v-if="selLink.transport !== 'icmp'" :label="$t('پورت حامل')" :hint="$t('پورتی که سمت شنونده روی آن گوش می‌دهد؛ با سرویس دیگری تداخل نداشته باشد.')">
            <input v-model.number="selLink.port" type="number" dir="ltr" min="1" max="65535" :disabled="!!selLink.tunnel" @input="changed" />
          </FormField>
          <FormField :label="$t('پورت‌های TCP منتقل‌شونده')" :hint="$t('مثال: 443 یا 443:2053 یا 8000-8010')">
            <input v-model="selLink.ports" type="text" dir="ltr" placeholder="443, 8443:2053, 8000-8010" :disabled="!!selLink.tunnel" @input="save" />
          </FormField>
          <FormField :label="$t('پورت‌های UDP (اختیاری)')"><input v-model="selLink.udp_ports" type="text" dir="ltr" placeholder="51820" :disabled="!!selLink.tunnel" @input="save" /></FormField>
          <div class="tp-row2">
            <FormField :label="$t('پریست سرعت')"><select v-model="selLink.preset" :disabled="!!selLink.tunnel" @change="save"><option v-for="[v, t] in PRESETS" :key="v" :value="v">{{ $t(t) }}</option></select></FormField>
            <FormField :label="$t('تعداد حامل')"><input v-model.number="selLink.carriers" type="number" min="1" max="16" :disabled="!!selLink.tunnel" @input="save" /></FormField>
          </div>
          <FormField :label="$t('مالتی‌پلکسر')" :hint="$t(MUX_HELP)">
            <select :value="selLink.mux_version || 1" :disabled="!!selLink.tunnel" @change="selLink.mux_version = Number($event.target.value); save()"><option v-for="[v, n] in MUX_VERSIONS" :key="v" :value="v">{{ $t(n) }}</option></select>
          </FormField>
        </template>
        <template v-else>
          <FormField :label="$t('نوع تانل کرنلی')" :hint="$t('نوع تانل کرنلی برای همهٔ کابل‌های کرنلی یک سرور ایران یکی است؛ پورت‌های هر کابل جداست.')">
            <select v-model="selLink.mode" :disabled="!!selLink.tunnel" @change="changed"><option v-for="[v, t] in KERNEL_MODES" :key="v" :value="v">{{ $t(t) }}</option></select>
          </FormField>
          <FormField :label="$t('پورت‌های TCP منتقل‌شونده')" :hint="$t('عدد، بازه یا 37826:37825 (پورت ایران به پورت سرور خارج)')"><input v-model="selLink.ports" type="text" dir="ltr" placeholder="37825, 62623" :disabled="!!selLink.tunnel" @input="save" /></FormField>
          <FormField :label="$t('پورت‌های UDP (اختیاری)')"><input v-model="selLink.udp_ports" type="text" dir="ltr" placeholder="51820" :disabled="!!selLink.tunnel" @input="save" /></FormField>
          <p class="muted">{{ $t('تانل کرنلی برای سرور مقابل به دسترسی SSH نیاز دارد.') }}</p>
        </template>

        <div class="actions">
          <button v-if="!selLink.tunnel" class="btn primary" :disabled="linkNotReady.length > 0" @click="runJobs([selLink])"><AppIcon name="play" /> {{ $t('اعمال این کابل') }}</button>
          <button v-if="selLink.tunnel" class="btn primary" @click="editCable = selLink.id"><AppIcon name="edit" /> {{ $t('ویرایش این تانل') }}</button>
          <button v-if="selLink.tunnel" class="btn" @click="runJobs([selLink])"><AppIcon name="redo" /> {{ $t('اعمال دوباره') }}</button>
          <button class="btn danger small" @click="removeSelected"><AppIcon name="trash" /> {{ selLink.tunnel ? $t('حذف کابل و تانل') : $t('حذف کابل') }}</button>
        </div>
        <div v-for="n in linkNotReady" :key="n.id" class="hint warn">{{ $t('سرور «{name}» هنوز اضافه و نصب نشده؛ روی آن کلیک کنید و «افزودن و نصب خودکار» را بزنید.', { name: nodeName(n) }) }}</div>
      </template>
      </div>
    </GlassWindow>

    <AddServerWindow v-if="adding" :node="adding.node" :watch="adding.watch" @close="adding = null; refresh()" @added="refresh()" />
    <CableEditWindow v-if="editCable" :id="editCable" @close="editCable = ''" @saved="refresh().catch(() => {})" />
    <ReaddressWindow v-if="moving" :node="moving" @close="moving = null" @done="refresh().catch(() => {})" />

    <GlassWindow v-if="jobs.open" :title="$t(jobs.title)" icon="play" :width="700" :closable="jobs.done" @close="jobs.open = false">
      <div class="tp-jobs">
        <div v-for="(r, i) in jobs.rows" :key="i" class="tp-job">
          <div class="tp-job-head"><b>{{ r.label }}</b>
            <span class="tp-job-st" :class="r.state">{{ r.state === 'run' ? $t('در حال اجرا…') : (r.state === 'ok' ? $t('انجام شد') : $t('خطا')) }}</span></div>
          <div class="tp-job-detail">
            <div v-for="(n, j) in r.notes" :key="'n' + j" :class="n.ok ? 'muted' : 'hint warn'">{{ $t(n.text) }}</div>
            <pre v-for="(o, j) in r.outputs" :key="'o' + j" class="code">{{ o }}</pre>
          </div>
        </div>
      </div>
      <template #foot><button class="btn" :disabled="!jobs.done" @click="jobs.open = false">{{ $t('بستن') }}</button></template>
    </GlassWindow>
  </div>
</template>

<style>
.tp-mode { position: absolute; z-index: 2; bottom: 14px; left: 50%; transform: translateX(-50%); display: flex; align-items: center; gap: 8px; padding: 7px 14px;
  border-radius: 999px; background: rgba(12, 15, 18, 0.88); border: 1px solid rgba(84, 94, 210, 0.45); color: var(--accent-2); font-size: 12.5px; pointer-events: none; white-space: nowrap; }
.tp-pal-item:hover .tp-pal-cable, .tp-pal-item.on .tp-pal-cable { animation: icDraw 0.8s ease; }
</style>
