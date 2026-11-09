<script setup>
import { computed } from 'vue';
import { TRANSPORTS, PRESETS, DEF_PORT, MUX_VERSIONS, MUX_HELP } from '../lib/constants.js';
import { KERNEL_MODES, cableType } from '../lib/fleet.js';
import SegControl from './SegControl.vue';
import FormField from './FormField.vue';

// The settings of a cable between an Iran and a Kharej server: its kind,
// transport, carrier port, speed and forwarded ports. It edits the object it
// is given in place; the edit window and the multi-location form share it.
const props = defineProps({
  l: { type: Object, required: true },
  withPorts: { type: Boolean, default: true }, // false: the caller asks per server
});
// every transport but ssh, which needs a login field a cable does not have
const TRANSPORT_OPTS = Object.entries(TRANSPORTS).filter(([k]) => k !== 'ssh');
const kind = computed(() => cableType(props.l));

function setKind(v) {
  const l = props.l;
  l.kind = v === 'kernel' ? 'kernel' : 'direct';
  l.reverse = v === 'reverse';
  if (v === 'kernel') { l.mode = l.mode || 'esp'; return; }
  l.transport = l.transport || 'tls';
  l.port = l.port || DEF_PORT[l.transport] || 443;
  l.preset = l.preset || 'balance';
  l.carriers = l.carriers || 2;
  l.mux_version = l.mux_version || 1;
}
// ICMP takes a single carrier; leaving it gives back the usual two
let lastTransport = props.l.transport;
function setTransport() {
  const l = props.l;
  if (l.transport !== 'icmp' && DEF_PORT[l.transport]) l.port = DEF_PORT[l.transport];
  if (l.transport === 'icmp') l.carriers = 1;
  else if (lastTransport === 'icmp' && (Number(l.carriers) || 0) <= 1) l.carriers = 2;
  lastTransport = l.transport;
}
</script>

<template>
  <div class="cable-fields">
    <FormField :label="$t('نوع کابل')"><SegControl :model-value="kind" :options="[['direct', $t('مستقیم')], ['reverse', $t('معکوس')], ['kernel', $t('کرنلی')]]" @update:model-value="setKind" /></FormField>
    <p v-if="l.kind === 'direct'" class="muted">{{ l.reverse
      ? $t('معکوس: سرور خارج به سرور ایران وصل می‌شود؛ پورت حامل روی ایران باز می‌شود و روی خارج پورت ورودی لازم نیست. برای خارجی که پشت فایروال است یا IP ورودی‌اش بسته است.')
      : $t('مستقیم: سرور ایران به سرور خارج وصل می‌شود؛ پورت حامل روی خارج باز می‌شود.') }}</p>

    <template v-if="l.kind === 'direct'">
      <div class="row">
        <FormField :label="$t('ترنسپورت')" :hint="$t((TRANSPORTS[l.transport] || {}).help)">
          <select v-model="l.transport" @change="setTransport"><option v-for="[k, v] in TRANSPORT_OPTS" :key="k" :value="k">{{ v.name }}</option></select>
        </FormField>
        <FormField v-if="l.transport !== 'icmp'" :label="$t('پورت حامل')" :hint="$t('پورتی که سمت شنونده روی آن گوش می‌دهد؛ با سرویس دیگری تداخل نداشته باشد.')">
          <input v-model.number="l.port" type="number" dir="ltr" min="1" max="65535" />
        </FormField>
      </div>
      <template v-if="withPorts">
        <FormField :label="$t('پورت‌های TCP منتقل‌شونده')" :hint="$t('مثال: 443 یا 443:2053 یا 8000-8010')">
          <input v-model="l.ports" type="text" dir="ltr" placeholder="443, 8443:2053, 8000-8010" />
        </FormField>
        <FormField :label="$t('پورت‌های UDP (اختیاری)')"><input v-model="l.udp_ports" type="text" dir="ltr" placeholder="51820" /></FormField>
      </template>
      <div class="row">
        <FormField :label="$t('پریست سرعت')"><select v-model="l.preset"><option v-for="[v, n] in PRESETS" :key="v" :value="v">{{ $t(n) }}</option></select></FormField>
        <FormField :label="$t('تعداد حامل')"><input v-model.number="l.carriers" type="number" min="1" max="16" :disabled="l.transport === 'icmp'" /></FormField>
      </div>
      <FormField :label="$t('مالتی‌پلکسر')" :hint="$t(MUX_HELP)">
        <select v-model.number="l.mux_version"><option v-for="[v, n] in MUX_VERSIONS" :key="v" :value="v">{{ $t(n) }}</option></select>
      </FormField>
    </template>
    <template v-else>
      <FormField :label="$t('نوع تانل کرنلی')" :hint="$t('نوع تانل کرنلی برای همهٔ کابل‌های کرنلی یک سرور ایران یکی است؛ پورت‌های هر کابل جداست.')">
        <select v-model="l.mode"><option v-for="[v, n] in KERNEL_MODES" :key="v" :value="v">{{ $t(n) }}</option></select>
      </FormField>
      <template v-if="withPorts">
        <FormField :label="$t('پورت‌های TCP منتقل‌شونده')" :hint="$t('عدد، بازه یا 37826:37825 (پورت ایران به پورت سرور خارج)')"><input v-model="l.ports" type="text" dir="ltr" placeholder="37825, 62623" /></FormField>
        <FormField :label="$t('پورت‌های UDP (اختیاری)')"><input v-model="l.udp_ports" type="text" dir="ltr" placeholder="51820" /></FormField>
      </template>
    </template>
  </div>
</template>
