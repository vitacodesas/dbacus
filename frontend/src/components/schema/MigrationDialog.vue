<script setup lang="ts">
// Asistente de migración de schema (spec 23). Solo pinta: Go inspecciona, planifica, copia y cuenta.
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { writeClipboard } from "../../composables/clipboard";
import { useConnectionsStore } from "../../stores/connections";
import { useMigrationStore, type MigrationStep } from "../../stores/migration";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import type { MigrationWarning } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";
import CodeEditor from "../ui/CodeEditor.vue";

const { t, te, locale } = useI18n();
const m = useMigrationStore();
const sessions = useSessionsStore();
const connections = useConnectionsStore();
const ui = useUiStore();

const open = computed({ get: () => m.isOpen, set: (v: boolean) => !v && m.close() });
const STEPS: MigrationStep[] = ["target", "content", "plan", "run"];
const stepIndex = computed(() => STEPS.indexOf(m.step));
const num = (n: number) => n.toLocaleString(locale.value);

const sessionOptions = computed(() =>
  sessions.list.map((s) => {
    const p = connections.byId.get(s.profileId);
    const name = p?.name ?? s.profileId;
    return { value: s.sessionId, label: p?.readOnly ? t("migration.target.readOnlyConn", { name }) : name, disabled: !!p?.readOnly };
  }),
);
const sameServer = computed(() => m.targetSessionId === m.sourceSessionId);

// Un error de Go se muestra con el texto del asistente si lo conocemos (por su detalle) y, si no, con el mensaje de Go.
const errorText = computed(() => {
  const e = m.error;
  if (!e) return "";
  const key = `migration.err.${e.detail}`;
  if (!e.detail || !te(key)) return e.message;
  // En estos tres el mensaje de Go trae lo concreto (qué tablas, qué privilegio): se añade al texto general.
  return ["exists", "incompatible", "privilege"].includes(e.detail) ? `${t(key)} — ${e.message}` : t(key);
});

// ---- selección de contenido ----
const emptyTables = computed(() => (m.plan?.tables ?? []).filter((x) => x.structureOnly).length);
const tableNames = computed(() => (m.catalog?.tables ?? []).filter((x) => !x.skipped).map((x) => x.name));
const skippedNames = computed(() => (m.catalog?.tables ?? []).filter((x) => x.skipped).map((x) => x.name));
const viewItems = computed(() => (m.catalog?.views ?? []).filter((x) => !x.skipped));
const routineItems = computed(() => (m.catalog?.routines ?? []).filter((x) => !x.skipped));
const triggerItems = computed(() => (m.catalog?.triggers ?? []).filter((x) => !x.skipped));
const routineKeys = computed(() => routineItems.value.map((r) => `${r.kind}:${r.name}`));
/** Vistas, rutinas y triggers que ya existen en el destino: se dejan como están y se dice si su código es igual. */
const existingObjects = (list: { name: string; kind: string; skipped: boolean; compare: string }[] | null | undefined) => (list ?? []).filter((x) => x.skipped);
const unreadableCount = computed(() => [...(m.catalog?.views ?? []), ...(m.catalog?.routines ?? []), ...(m.catalog?.triggers ?? [])].filter((x) => x.compare === "unreadable").length);
const pickedNothing = computed(() => m.pickedTables.length === 0 && m.pickedViews.length === 0 && m.pickedRoutines.length === 0 && m.pickedTriggers.length === 0);

// ---- plan ----
const warnings = computed(() => m.plan?.warnings ?? []);
const planObjects = computed(() => [...(m.plan?.routines ?? []), ...(m.plan?.views ?? []), ...(m.plan?.triggers ?? [])]);
const wClass = (w: MigrationWarning) => (w.level === "danger" ? "border-danger text-danger" : w.level === "warning" ? "border-warning" : "border-border text-muted");
const statementsText = computed(() => (m.plan?.statements ?? []).join("\n\n"));

// ---- progreso ----
const job = computed(() => m.job);
const percent = computed(() => {
  const j = job.value;
  if (!j) return 0;
  if (j.state === "done") return 100;
  if (j.estRows <= 0) return j.tablesAll > 0 ? Math.floor((j.tablesDone / j.tablesAll) * 100) : 0;
  return Math.min(99, Math.floor((j.rows / j.estRows) * 100));
});
const rate = computed(() => (job.value && job.value.elapsedMs > 1000 ? Math.round(job.value.rows / (job.value.elapsedMs / 1000)) : 0));
const clock = (ms: number) => {
  const s = Math.max(0, Math.floor(ms / 1000));
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
};
const eta = computed(() => {
  const j = job.value;
  if (!j || j.state !== "running" || rate.value <= 0 || j.estRows <= j.rows) return "";
  return clock(((j.estRows - j.rows) / rate.value) * 1000);
});
const phaseLabel = computed(() => (job.value?.phase ? t(`migration.run.phase.${job.value.phase}`) : ""));
const failed = computed(() => m.result?.failed ?? []);
const verification = computed(() => m.result?.verification ?? null);
const objectDiffs = computed(() => (verification.value?.objects ?? []).filter((o) => o.status !== "same"));
const created = computed(() => m.result?.created ?? []);

const confirm = ref("");
const cleanupOpen = ref(false);
watch(() => m.isOpen, (v) => { if (v) { confirm.value = ""; cleanupOpen.value = false; } });
async function doCleanup() {
  if (await m.cleanup(confirm.value)) {
    cleanupOpen.value = false;
    confirm.value = "";
  }
}

async function copyReport() {
  const r = m.result;
  if (!r) return;
  const lines = [`${m.sourceDb} → ${m.targetDb}: ${t(`migration.run.state.${r.job.state}`)}`, `${num(r.job.rows)} filas`, ...(r.job.tables ?? []).map((x) => `${x.name}: ${t(`migration.run.table.${x.state}`)} (${num(x.rowsDone)})`), ...(r.failed ?? []).map((f) => `✗ ${f.object}: ${f.error}`)];
  if (await writeClipboard(lines.join("\n"))) ui.toast("success", t("history.copied"));
}

const canNext = computed(() => {
  if (m.busy) return false;
  if (m.step === "target") return !!m.targetSessionId && m.targetDb.trim() !== "";
  if (m.step === "content") return !pickedNothing.value;
  return true;
});
function next() {
  if (m.step === "target") void m.loadCatalog();
  else if (m.step === "content") void m.loadPlan();
}
const workersModel = computed({ get: () => m.workers, set: (v: string | number) => (m.workers = Number(v) || 0) });
const chunkModel = computed({ get: () => m.chunkRows, set: (v: string | number) => (m.chunkRows = Number(v) || 0) });
const structureOnlyPicked = (n: string) => m.pickedTables.includes(n) && m.structureOnly.includes(n);
const tableLine = (n: string) => (m.catalog?.tables ?? []).find((x) => x.name === n);
</script>

<template>
  <BaseModal v-model:open="open" :title="`${t('migration.title')} · ${m.sourceDb}`" :description="t('migration.readOnlyNote')" size="xl">
    <div class="flex flex-col gap-4" data-testid="migration-dialog">
      <ol class="flex gap-2 text-xs" aria-label="pasos">
        <li v-for="(s, i) in STEPS" :key="s" class="rounded px-2 py-1" :class="i === stepIndex ? 'bg-accent/10 font-semibold text-fg' : i < stepIndex ? 'text-muted' : 'text-muted opacity-60'" :data-testid="`migration-step-${s}`" :aria-current="i === stepIndex ? 'step' : undefined">
          {{ i + 1 }}. {{ t(`migration.steps.${s}`) }}
        </li>
      </ol>

      <p v-if="m.error" class="rounded-md border border-danger p-2 text-sm text-danger" role="alert" data-testid="migration-error">{{ errorText }}</p>

      <!-- 1. Destino -->
      <section v-if="m.step === 'target'" class="flex flex-col gap-3" data-testid="migration-step-target-body">
        <p class="text-sm"><span class="text-muted">{{ t("migration.target.source") }}:</span> <span class="font-data">{{ m.sourceDb }}</span></p>
        <BaseSelect v-model="m.targetSessionId" :label="t('migration.target.connection')" :options="sessionOptions" data-testid="migration-target-session" />
        <p class="text-xs text-muted" data-testid="migration-route-hint">{{ sameServer ? t("migration.target.sameServer") : t("migration.target.otherConn") }}</p>
        <BaseInput v-model="m.targetDb" :label="t('migration.target.database')" data-testid="migration-target-db" />
        <BaseCheckbox v-model="m.skipExisting" :label="t('migration.target.skipExisting')" data-testid="migration-skip-existing" />
        <p class="text-xs text-muted">{{ t("migration.target.hint") }}</p>
      </section>

      <!-- 2. Contenido -->
      <section v-else-if="m.step === 'content'" class="flex flex-col gap-4" data-testid="migration-step-content-body">
        <fieldset class="flex flex-col gap-1">
          <legend class="flex items-center gap-2 text-xs font-semibold text-muted">
            {{ t("migration.content.tables") }} ({{ m.pickedTables.length }}/{{ tableNames.length }})
            <button type="button" class="text-accent underline" @click="m.pickedTables = [...tableNames]">{{ t("migration.all") }}</button>
            <button type="button" class="text-accent underline" @click="m.pickedTables = []">{{ t("migration.none") }}</button>
            <span class="ml-2 font-normal">{{ t("migration.content.structureOnly") }}:</span>
            <button type="button" class="text-accent underline" data-testid="migration-structure-all" @click="m.structureOnly = [...m.pickedTables]">{{ t("migration.content.structureAll") }}</button>
            <button type="button" class="text-accent underline" data-testid="migration-structure-none" @click="m.structureOnly = []">{{ t("migration.content.structureNone") }}</button>
          </legend>
          <div v-for="n in tableNames" :key="n" class="flex items-center gap-2 text-sm">
            <label class="flex items-center gap-2" :data-testid="`migration-pick-table-${n}`">
              <input v-model="m.pickedTables" type="checkbox" :value="n" class="size-4 accent-[var(--accent)]">
              <span class="font-data">{{ n }}</span>
              <span class="text-xs text-muted" :class="{ 'line-through': structureOnlyPicked(n) }">{{ t("migration.content.rows", { n: num(tableLine(n)?.estRows ?? 0) }) }}</span>
            </label>
            <label class="ml-auto flex items-center gap-1 text-xs text-muted" :class="{ 'opacity-50': !m.pickedTables.includes(n) }" :data-testid="`migration-structure-${n}`">
              <input v-model="m.structureOnly" type="checkbox" :value="n" :disabled="!m.pickedTables.includes(n)" :aria-label="t('migration.content.structureOnlyOf', { name: n })" class="size-3.5 accent-[var(--accent)]">
              {{ t("migration.content.structureOnly") }}
            </label>
          </div>
          <p v-for="n in skippedNames" :key="n" class="text-xs text-muted"><span class="font-data">{{ n }}</span> · {{ t("migration.content.skipped") }}</p>
        </fieldset>
        <fieldset v-if="(m.catalog?.views ?? []).length" class="flex flex-col gap-1">
          <legend class="flex items-center gap-2 text-xs font-semibold text-muted">
            {{ t("migration.content.views") }} ({{ m.pickedViews.length }}/{{ viewItems.length }})
            <button type="button" class="text-accent underline" @click="m.pickedViews = viewItems.map((x) => x.name)">{{ t("migration.all") }}</button>
            <button type="button" class="text-accent underline" @click="m.pickedViews = []">{{ t("migration.none") }}</button>
          </legend>
          <label v-for="v in viewItems" :key="v.name" class="flex items-center gap-2 text-sm" :data-testid="`migration-pick-view-${v.name}`"><input v-model="m.pickedViews" type="checkbox" :value="v.name" class="size-4 accent-[var(--accent)]"><span class="font-data">{{ v.name }}</span></label>
          <p v-for="o in existingObjects(m.catalog?.views)" :key="o.kind + o.name" class="text-xs" :class="o.compare === 'unreadable' ? 'text-warning' : 'text-muted'" :data-testid="`migration-existing-${o.name}`"><span class="font-data">{{ o.name }}</span> · {{ t(`migration.content.existing.${o.compare || 'unknown'}`) }}</p>
        </fieldset>
        <fieldset v-if="(m.catalog?.routines ?? []).length" class="flex flex-col gap-1">
          <legend class="flex items-center gap-2 text-xs font-semibold text-muted">
            {{ t("migration.content.routines") }} ({{ m.pickedRoutines.length }}/{{ routineKeys.length }})
            <button type="button" class="text-accent underline" @click="m.pickedRoutines = [...routineKeys]">{{ t("migration.all") }}</button>
            <button type="button" class="text-accent underline" @click="m.pickedRoutines = []">{{ t("migration.none") }}</button>
          </legend>
          <label v-for="r in routineItems" :key="r.kind + r.name" class="flex items-center gap-2 text-sm" :data-testid="`migration-pick-routine-${r.name}`"><input v-model="m.pickedRoutines" type="checkbox" :value="`${r.kind}:${r.name}`" class="size-4 accent-[var(--accent)]"><span class="font-data">{{ r.name }}</span><span class="text-xs text-muted">{{ r.kind }}</span></label>
          <p v-for="o in existingObjects(m.catalog?.routines)" :key="o.kind + o.name" class="text-xs text-muted" :data-testid="`migration-existing-${o.name}`"><span class="font-data">{{ o.name }}</span> · {{ t(`migration.content.existing.${o.compare || 'unknown'}`) }}</p>
        </fieldset>
        <fieldset v-if="(m.catalog?.triggers ?? []).length" class="flex flex-col gap-1">
          <legend class="flex items-center gap-2 text-xs font-semibold text-muted">
            {{ t("migration.content.triggers") }} ({{ m.pickedTriggers.length }}/{{ triggerItems.length }})
            <button type="button" class="text-accent underline" @click="m.pickedTriggers = triggerItems.map((x) => x.name)">{{ t("migration.all") }}</button>
            <button type="button" class="text-accent underline" @click="m.pickedTriggers = []">{{ t("migration.none") }}</button>
          </legend>
          <label v-for="tr in triggerItems" :key="tr.name" class="flex items-center gap-2 text-sm" :data-testid="`migration-pick-trigger-${tr.name}`"><input v-model="m.pickedTriggers" type="checkbox" :value="tr.name" class="size-4 accent-[var(--accent)]"><span class="font-data">{{ tr.name }}</span><span class="text-xs text-muted">{{ tr.table }}</span></label>
          <p v-for="o in existingObjects(m.catalog?.triggers)" :key="o.kind + o.name" class="text-xs text-muted" :data-testid="`migration-existing-${o.name}`"><span class="font-data">{{ o.name }}</span> · {{ t(`migration.content.existing.${o.compare || 'unknown'}`) }}</p>
        </fieldset>
        <p v-if="pickedNothing" class="text-sm text-warning" data-testid="migration-empty">{{ unreadableCount > 0 ? t("migration.content.emptyUnreadable", { n: unreadableCount }) : t("migration.content.empty") }}</p>
        <details class="rounded-md border border-border p-2" data-testid="migration-advanced">
          <summary class="cursor-pointer text-sm">{{ t("migration.content.advanced") }}</summary>
          <div class="mt-2 grid grid-cols-2 gap-3">
            <div><BaseInput v-model="workersModel" type="number" :label="t('migration.content.workers')" data-testid="migration-workers" /><p class="mt-1 text-xs text-muted">{{ t("migration.content.workersHint") }}</p></div>
            <div><BaseInput v-model="chunkModel" type="number" :label="t('migration.content.chunkRows')" data-testid="migration-chunk-rows" /><p class="mt-1 text-xs text-muted">{{ t("migration.content.chunkHint") }}</p></div>
          </div>
          <div class="mt-3 flex flex-col gap-1">
            <BaseCheckbox v-model="m.verifyForeignKeys" :label="t('migration.content.verifyFk')" data-testid="migration-verify-fk" />
            <BaseCheckbox v-model="m.verifyRowCounts" :label="t('migration.content.verifyRows')" data-testid="migration-verify-rows" />
            <p class="text-xs text-muted">{{ t("migration.content.verifyHint") }}</p>
          </div>
        </details>
      </section>

      <!-- 3. Plan -->
      <section v-else-if="m.step === 'plan' && m.plan" class="flex flex-col gap-3" data-testid="migration-step-plan-body">
        <dl class="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-4">
          <div><dt class="text-xs text-muted">{{ t("migration.plan.route") }}</dt><dd data-testid="migration-plan-route">{{ m.plan.route === "server" ? t("migration.plan.routeServer") : t("migration.plan.routeGo") }}</dd></div>
          <div><dt class="text-xs text-muted">{{ t("migration.plan.workers") }}</dt><dd data-testid="migration-plan-workers">{{ m.plan.workers }}</dd></div>
          <div><dt class="text-xs text-muted">{{ t("migration.content.tables") }}</dt><dd>{{ t("migration.plan.tables", { n: (m.plan.tables ?? []).filter((x) => !x.skipped).length }) }} · {{ t("migration.plan.chunks", { n: (m.plan.tables ?? []).reduce((a, x) => a + x.chunks, 0) }) }}<template v-if="emptyTables > 0"> · <span data-testid="migration-plan-structure">{{ t("migration.plan.structureOnly", { n: emptyTables }) }}</span></template></dd></div>
          <div><dt class="text-xs text-muted">&nbsp;</dt><dd class="text-xs text-muted">{{ t("migration.plan.connections", { free: m.plan.serverCaps.freeConnections, max: m.plan.serverCaps.maxConnections }) }}</dd></div>
        </dl>
        <div v-if="planObjects.length" class="flex flex-col gap-1">
          <h3 class="text-xs font-semibold text-muted">{{ t("migration.plan.objects", { n: planObjects.filter((o) => !o.skipped).length }) }}</h3>
          <ul class="flex max-h-40 flex-col gap-0.5 overflow-auto text-sm" data-testid="migration-plan-objects">
            <li v-for="o in planObjects" :key="o.kind + o.name" class="flex items-baseline gap-2" :data-testid="`migration-plan-object-${o.name}`">
              <span class="font-data">{{ o.name }}</span>
              <span class="text-xs text-muted">{{ t(`migration.run.kind.${o.kind}`) }}</span>
              <span class="text-xs" :class="o.skipped ? (o.compare === 'unreadable' ? 'text-warning' : 'text-muted') : ''">{{ o.skipped ? t(`migration.content.existing.${o.compare || 'unknown'}`) : t("migration.plan.objectCreate") }}</span>
            </li>
          </ul>
        </div>
        <p v-if="m.plan.targetNew" class="text-sm" data-testid="migration-plan-new">{{ t("migration.plan.newSchema", { db: m.targetDb }) }}</p>
        <div class="flex flex-col gap-1">
          <h3 class="text-xs font-semibold text-muted">{{ t("migration.plan.warnings") }}</h3>
          <ul v-if="warnings.length" class="flex flex-col gap-1" data-testid="migration-warnings">
            <li v-for="(w, i) in warnings" :key="i" class="rounded border-l-2 px-2 py-1 text-xs" :class="wClass(w)" :data-testid="`migration-warning-${w.code}`">
              <span v-if="w.object" class="font-data">{{ w.object }}: </span>{{ w.message }}
            </li>
          </ul>
          <p v-else class="text-xs text-muted">{{ t("migration.plan.noWarnings") }}</p>
        </div>
        <div class="flex flex-col gap-1">
          <h3 class="text-xs font-semibold text-muted">{{ t("migration.plan.statements") }}</h3>
          <div class="h-48 overflow-hidden rounded-md border border-border" data-testid="migration-statements"><CodeEditor :model-value="statementsText" read-only label="SQL de la migración" /></div>
        </div>
      </section>

      <!-- 4. Progreso y resultado -->
      <section v-else-if="m.step === 'run' && job" class="flex flex-col gap-3" data-testid="migration-step-run-body">
        <div class="flex items-center justify-between text-sm">
          <span data-testid="migration-state">{{ t(`migration.run.state.${job.state}`) }}</span>
          <span class="text-xs text-muted" data-testid="migration-phase">{{ m.running ? phaseLabel : "" }}</span>
        </div>
        <div class="h-2 overflow-hidden rounded bg-hover" role="progressbar" :aria-valuenow="percent" aria-valuemin="0" aria-valuemax="100" data-testid="migration-progress">
          <div class="h-full bg-accent transition-all" :style="{ width: `${percent}%` }" />
        </div>
        <p class="flex flex-wrap gap-x-4 text-xs text-muted" data-testid="migration-numbers">
          <span>{{ t("migration.run.rows", { rows: num(job.rows), est: num(Math.max(job.estRows, job.rows)) }) }}</span>
          <span v-if="rate">{{ t("migration.run.rate", { n: num(rate) }) }}</span>
          <span v-if="eta">{{ t("migration.run.eta", { t: eta }) }}</span>
          <span>{{ t("migration.run.elapsed", { t: clock(job.elapsedMs) }) }}</span>
        </p>
        <ul class="flex max-h-56 flex-col gap-0.5 overflow-auto text-sm" data-testid="migration-tables">
          <li v-for="tb in job.tables" :key="tb.name" class="flex items-baseline gap-2" :data-testid="`migration-table-${tb.name}`">
            <span class="font-data">{{ tb.name }}</span>
            <span class="text-xs" :class="tb.state === 'failed' ? 'text-danger' : 'text-muted'" :data-testid="`migration-table-state-${tb.name}`">{{ t(`migration.run.table.${tb.state}`) }}</span>
            <span v-if="tb.structureOnly && tb.state !== 'skipped'" class="text-xs text-muted" :data-testid="`migration-table-structure-${tb.name}`">{{ t("migration.run.structureOnly") }}</span>
            <span v-else-if="tb.state !== 'skipped' && tb.state !== 'pending'" class="text-xs text-muted">{{ num(tb.rowsDone) }} · {{ t("migration.run.chunks", { done: tb.chunksDone, all: tb.chunksAll }) }}</span>
          </li>
        </ul>

        <ul v-if="(job.objects ?? []).length" class="flex max-h-40 flex-col gap-0.5 overflow-auto text-sm" data-testid="migration-job-objects">
          <li v-for="o in job.objects" :key="o.kind + o.name" class="flex items-baseline gap-2" :data-testid="`migration-job-object-${o.name}`">
            <span class="font-data">{{ o.name }}</span>
            <span class="text-xs text-muted">{{ t(`migration.run.kind.${o.kind}`) }}</span>
            <span class="text-xs" :class="o.state === 'failed' ? 'text-danger' : 'text-muted'" :data-testid="`migration-job-object-state-${o.name}`">{{ t(`migration.run.object.${o.state}`) }}</span>
          </li>
        </ul>

        <template v-if="m.finished">
          <div v-if="failed.length" class="flex flex-col gap-1" data-testid="migration-failed">
            <h3 class="text-xs font-semibold text-danger">{{ t("migration.run.failedTitle") }}</h3>
            <ul class="flex flex-col gap-1">
              <li v-for="(f, i) in failed" :key="i" class="rounded border-l-2 border-danger px-2 py-1 text-xs"><span class="font-data">{{ f.object || f.kind }}</span>: {{ f.error }}</li>
            </ul>
          </div>
          <div v-if="verification" class="flex flex-col gap-1" data-testid="migration-verification">
            <h3 class="text-xs font-semibold text-muted">{{ t("migration.run.verification") }}</h3>
            <p v-if="!(verification.orphans ?? []).length && !(verification.rowCounts ?? []).length && !objectDiffs.length" class="text-sm text-success" data-testid="migration-verified-ok">{{ t("migration.run.verifiedOk") }}</p>
            <template v-else>
              <ul v-if="(verification.orphans ?? []).length" class="text-sm" data-testid="migration-orphans">
                <li v-for="o in verification.orphans" :key="o.table + o.foreignKey" class="text-danger">{{ t("migration.run.orphanLine", { table: o.table, fk: o.foreignKey, n: num(o.count) }) }}<span v-if="o.atLeast"> ({{ t("migration.run.atLeast", { n: num(o.count) }) }})</span></li>
              </ul>
              <ul v-if="(verification.rowCounts ?? []).length" class="text-sm" data-testid="migration-rowcounts">
                <li v-for="r in verification.rowCounts" :key="r.table" class="text-danger">{{ t("migration.run.rowLine", { table: r.table, src: num(r.source), dst: num(r.target) }) }}</li>
              </ul>
            </template>
            <ul v-if="(verification.objects ?? []).length" class="max-h-40 overflow-auto text-sm" data-testid="migration-objects">
              <li v-for="o in verification.objects" :key="o.kind + o.name" :class="o.status === 'same' ? 'text-muted' : 'text-danger'" :data-testid="`migration-object-${o.name}`">{{ t("migration.run.objectLine", { kind: t(`migration.run.kind.${o.kind}`), name: o.name, status: t(`migration.run.objStatus.${o.status}`) }) }}</li>
            </ul>
            <p v-if="(verification.objectsChecked ?? 0) > 0" class="text-xs text-muted" data-testid="migration-objects-checked">{{ t("migration.run.objectsChecked", { n: num(verification.objectsChecked) }) }}</p>
          </div>
          <div v-if="cleanupOpen" class="flex flex-col gap-2 rounded-md border border-danger p-3" data-testid="migration-cleanup-box">
            <h3 class="text-sm font-semibold">{{ t("migration.run.cleanupTitle") }}</h3>
            <p class="text-xs text-muted">{{ t("migration.run.cleanupHint") }}</p>
            <BaseInput v-model="confirm" :placeholder="m.targetDb" data-testid="migration-cleanup-confirm" />
            <div class="flex justify-end gap-2">
              <BaseButton size="sm" @click="cleanupOpen = false">{{ t("migration.cancel") }}</BaseButton>
              <BaseButton size="sm" variant="danger" :disabled="confirm !== m.targetDb" :loading="m.cleaning" data-testid="migration-cleanup-go" @click="doCleanup">{{ t("migration.run.cleanupConfirm") }}</BaseButton>
            </div>
          </div>
        </template>
        <p v-else class="text-xs text-muted">{{ t("migration.run.bg") }}</p>
      </section>
    </div>

    <template #footer>
      <template v-if="m.step === 'run'">
        <template v-if="m.running">
          <BaseButton :disabled="job?.state === 'cancelling'" variant="danger" data-testid="migration-cancel" @click="m.cancel()">{{ t("migration.cancel") }}</BaseButton>
          <BaseButton data-testid="migration-close" @click="m.close()">{{ t("migration.close") }}</BaseButton>
        </template>
        <template v-else>
          <BaseButton v-if="failed.length" data-testid="migration-retry" @click="m.retry()">{{ t("migration.run.retry") }}</BaseButton>
          <BaseButton :loading="m.verifying" data-testid="migration-verify" @click="m.verify()">{{ t("migration.run.verify") }}</BaseButton>
          <BaseButton v-if="created.length" data-testid="migration-cleanup" @click="cleanupOpen = !cleanupOpen">{{ t("migration.run.cleanup") }}</BaseButton>
          <BaseButton data-testid="migration-copy" @click="copyReport">{{ t("migration.run.copyReport") }}</BaseButton>
          <BaseButton variant="primary" data-testid="migration-close" @click="m.close()">{{ t("migration.close") }}</BaseButton>
        </template>
      </template>
      <template v-else>
        <BaseButton data-testid="migration-close" @click="m.close()">{{ t("migration.cancel") }}</BaseButton>
        <BaseButton v-if="m.step !== 'target'" :disabled="m.busy" data-testid="migration-back" @click="m.back()">{{ t("migration.back") }}</BaseButton>
        <BaseButton v-if="m.step !== 'plan'" variant="primary" :disabled="!canNext" :loading="m.busy" data-testid="migration-next" @click="next">{{ t("migration.next") }}</BaseButton>
        <BaseButton v-else variant="primary" :loading="m.busy" data-testid="migration-start" @click="m.start()">{{ t("migration.migrate") }}</BaseButton>
      </template>
    </template>
  </BaseModal>
</template>
