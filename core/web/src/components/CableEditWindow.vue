<script setup>
import { ref, reactive, onMounted } from 'vue';
import { api } from '../lib/api.js';
import { fail, toast } from '../lib/toast.js';
import { confirmBox } from '../lib/confirm.js';
import { t, fwd } from '../lib/i18n.js';
import { nodeName, cableType } from '../lib/fleet.js';
import GlassWindow from './GlassWindow.vue';
import CableFields from './CableFields.vue';
import AppIcon from './AppIcon.vue';

// Edits a tunnel the panel built between two servers: new ports, transport,
// direction or kind. Saving rebuilds the tunnel on both servers.
const props = defineProps({ id: { type: String, required: true } });
const emit = defineEmits(['close', 'saved']);

const l = reactive({});
const ends = ref('');
const loaded = ref(false);
const busy = ref(false);
const out = ref('');
let was = '';

const steps = (results) => (results || []).map((s) => (s.ok ? '✅ ' : '❌ ') + s.step + '\n' + (s.error ? s.error + '\n' : '') + (s.output || '').trim()).join('\n\n');

onMounted(async () => {
  try {
    const f = await api('GET', '/api/fleet');
    const x = (f.links || []).find((k) => k.id === props.id);
    if (!x) throw new Error(t('این کابل پیدا نشد؛ صفحه را تازه کنید'));
    const byId = Object.fromEntries(f.nodes.map((n) => [n.id, n]));
    const [a, b] = [byId[x.a] || {}, byId[x.b] || {}];
    const [ir, kh] = a.role === 'kharej' ? [b, a] : [a, b];
    ends.value = nodeName(ir) + ' ' + fwd() + ' ' + nodeName(kh);
    Object.assign(l, { kind: x.kind, ports: x.ports || '', udp_ports: x.udp_ports || '', reverse: !!x.reverse, mode: x.mode || 'esp',
      transport: x.transport || 'tls', port: x.port || '', preset: x.preset || 'balance', carriers: x.carriers || 2, mux_version: x.mux_version || 1 });
    was = cableType(x);
    loaded.value = true;
  } catch (e) { fail(e); emit('close'); }
});

async function save() {
  if (cableType(l) !== was && !(await confirmBox(t('تغییر نوع تانل'),
    t('تانل فعلی از روی هر دو سرور برداشته و تانل تازه ساخته می‌شود.'), t('ادامه')))) return;
  busy.value = true;
  out.value = t('در حال ساخت دوباره روی هر دو سرور…');
  try {
    const r = await api('POST', '/api/fleet/edit', { id: props.id, ...l, port: Number(l.port) || 0, carriers: Number(l.carriers) || 0 });
    out.value = steps(r.results);
    was = cableType(l);
    toast(t('تانل ویرایش و دوباره ساخته شد'), 'ok');
  } catch (e) {
    // the per-step results of a failed run ride on the error
    out.value = (e.data && e.data.results) ? steps(e.data.results) : '';
    fail(e);
  }
  busy.value = false;
  emit('saved');
}
</script>

<template>
  <GlassWindow :title="$t('ویرایش تانل {ends}', { ends })" icon="edit" :width="680" :closable="!busy" @close="emit('close')">
    <div v-if="!loaded" class="empty"><span class="spin"></span></div>
    <div v-else class="cable-edit">
      <CableFields :l="l" />
      <p class="hint">{{ $t('با ذخیره، تانل با تنظیمات تازه روی هر دو سرور دوباره ساخته می‌شود و کاربرانش چند ثانیه قطع می‌شوند.') }}</p>
      <pre v-if="out" class="logs">{{ out }}</pre>
      <div class="form-actions">
        <button type="button" class="btn primary" :disabled="busy" @click="save"><span v-if="busy" class="spin"></span><AppIcon v-else name="redo" /> {{ $t('ذخیره و ساخت دوباره') }}</button>
        <button type="button" class="btn" :disabled="busy" @click="emit('close')">{{ $t('بستن') }}</button>
      </div>
    </div>
  </GlassWindow>
</template>
