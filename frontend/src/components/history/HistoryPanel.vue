<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Check, CornerDownLeft, Copy, History as HistoryIcon, Search, SquareTerminal, Trash2, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { asAppError } from "../../api/errors";
import { useConnectionsStore } from "../../stores/connections";
import { useHistoryStore, type HistoryStatus } from "../../stores/history";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import type { HistoryEntry } from "../../types";
import { formatDuration, relativeTime } from "../../utils/time";
import BaseButton from "../ui/BaseButton.vue";
import BaseDialogConfirm from "../ui/BaseDialogConfirm.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const { t, locale } = useI18n();
const history = useHistoryStore();
const sessions = useSessionsStore();
const tabs = useQueryTabsStore();
const connections = useConnectionsStore();
const ui = useUiStore();

// El historial es de una conexión: la de la sesión activa o, si no hay, la de la pestaña abierta.
const profileId = computed(() => sessions.active?.profileId ?? tabs.active?.profileId ?? null);
const profile = computed(() => connections.profiles.find((p) => p.id === profileId.value) ?? null);
watch(profileId, (id) => history.setProfile(id), { immediate: true });

const query = ref(history.search);
let debounce: ReturnType<typeof setTimeout> | null = null;
watch(query, (q) => {
  if (debounce) clearTimeout(debounce);
  debounce = setTimeout(() => history.setFilters({ search: q.trim() }), 250);
});
onBeforeUnmount(() => debounce && clearTimeout(debounce));

const filters: { value: HistoryStatus; label: string }[] = [
  { value: "", label: "history.filterAll" },
  { value: "ok", label: "history.filterOk" },
  { value: "error", label: "history.filterError" },
];
const filtering = computed(() => !!history.search || !!history.status);

const clearOpen = ref(false);
const oneLine = (sql: string) => sql.replace(/\s+/g, " ").trim();

function openInTab(e: HistoryEntry) {
  if (!profileId.value) return;
  tabs.openTab({ profileId: profileId.value, sql: e.sql, database: e.database });
}
function insert(e: HistoryEntry) {
  const tab = tabs.active;
  if (!tab || tab.profileId !== profileId.value) return openInTab(e);
  tabs.setSql(tab.id, tab.sql.trim() ? `${tab.sql.replace(/\s+$/, "")}\n\n${e.sql}` : e.sql);
}
async function copy(e: HistoryEntry) {
  try {
    await navigator.clipboard.writeText(e.sql);
    ui.toast("success", t("history.copied"));
  } catch {
    ui.toast("error", t("history.cannotCopy"));
  }
}
async function remove(e: HistoryEntry) {
  try {
    await history.remove(e.id);
  } catch (err) {
    ui.toast("error", asAppError(err).message);
  }
}
async function clearAll() {
  try {
    await history.clear();
  } catch (err) {
    ui.toast("error", asAppError(err).message);
  }
}
const summary = (e: HistoryEntry) => {
  if (!e.ok) return t("history.failed");
  if (e.rowCount > 0) return t("history.rows", { n: e.rowCount.toLocaleString() });
  if (e.affectedRows > 0) return t("history.affected", { n: e.affectedRows.toLocaleString() });
  return t("history.ok");
};
</script>

<template>
  <section class="flex h-full min-h-0 flex-col" :aria-label="t('history.title')" data-testid="history-panel">
    <header class="flex items-center justify-between px-3 py-2">
      <h2 class="truncate text-xs font-semibold tracking-wide text-muted uppercase">{{ t("history.title") }}<template v-if="profile"> · {{ profile.name }}</template></h2>
      <BaseButton size="sm" variant="ghost" :disabled="!history.entries.length && !filtering" :aria-label="t('history.clear')" :title="t('history.clear')" @click="clearOpen = true">
        <Trash2 class="size-4" />
      </BaseButton>
    </header>

    <BaseEmptyState v-if="!profileId" :title="t('history.noConnection')"><template #icon><HistoryIcon class="size-8" /></template></BaseEmptyState>

    <template v-else>
      <div class="flex flex-col gap-2 px-3 pb-2">
        <label class="relative block">
          <Search class="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted" />
          <input v-model="query" type="search" class="w-full rounded-md border border-border bg-bg py-1 pr-2 pl-7 text-sm" :placeholder="t('history.search')" :aria-label="t('history.search')">
        </label>
        <div class="flex gap-1" role="group" :aria-label="t('history.filter')">
          <button
            v-for="f in filters"
            :key="f.value"
            class="rounded-md px-2 py-0.5 text-xs"
            :class="history.status === f.value ? 'bg-hover text-fg' : 'text-muted hover:text-fg'"
            :aria-pressed="history.status === f.value"
            @click="history.setFilters({ status: f.value })"
          >
            {{ t(f.label) }}
          </button>
        </div>
        <p v-if="profile?.noHistory" class="rounded-md bg-warning/10 px-2 py-1 text-xs" role="status">{{ t("history.disabled") }}</p>
      </div>

      <div v-if="history.error" class="mx-3 rounded-md border border-danger p-2 text-sm" role="alert">
        {{ history.error }}
        <BaseButton size="sm" class="ml-2" @click="history.reload()">{{ t("connections.retry") }}</BaseButton>
      </div>

      <ul v-if="history.entries.length" class="min-h-0 flex-1 overflow-auto px-2 pb-2" data-testid="history-list">
        <li v-for="e in history.entries" :key="e.id" class="group relative rounded-md px-2 py-1.5 select-none hover:bg-hover focus-within:bg-hover" data-testid="history-item" @dblclick="openInTab(e)">
          <div class="flex items-center gap-1.5 text-[11px] text-muted">
            <Check v-if="e.ok" class="size-3.5 shrink-0 text-success" :aria-label="t('history.ok')" />
            <X v-else class="size-3.5 shrink-0 text-danger" :aria-label="t('history.failed')" />
            <span :title="new Date(e.executedAt).toLocaleString(locale)">{{ relativeTime(e.executedAt, locale) }}</span>
            <span class="font-data">· {{ formatDuration(e.elapsedMs, locale) }}</span>
            <span>· {{ summary(e) }}</span>
            <span v-if="e.database" class="font-data ml-auto truncate">{{ e.database }}</span>
          </div>
          <div class="font-data mt-0.5 line-clamp-2 text-xs break-all" :title="e.sql">{{ oneLine(e.sql) }}</div>
          <div v-if="!e.ok && e.errorMessage" class="mt-0.5 line-clamp-2 text-xs text-danger">{{ e.errorMessage }}</div>
          <div class="absolute top-1 right-1 hidden gap-0.5 rounded-md border border-border bg-panel p-0.5 group-focus-within:flex group-hover:flex">
            <button class="rounded p-1 hover:bg-hover" :title="t('history.openTab')" :aria-label="t('history.openTab')" @click="openInTab(e)"><SquareTerminal class="size-3.5" /></button>
            <button class="rounded p-1 hover:bg-hover" :title="t('history.insert')" :aria-label="t('history.insert')" @click="insert(e)"><CornerDownLeft class="size-3.5" /></button>
            <button class="rounded p-1 hover:bg-hover" :title="t('history.copy')" :aria-label="t('history.copy')" @click="copy(e)"><Copy class="size-3.5" /></button>
            <button class="rounded p-1 text-danger hover:bg-hover" :title="t('history.delete')" :aria-label="t('history.delete')" @click="remove(e)"><Trash2 class="size-3.5" /></button>
          </div>
        </li>
        <li v-if="history.nextCursor" class="px-2 py-2"><BaseButton size="sm" class="w-full" :disabled="history.loading" @click="history.more()">{{ t("history.loadMore") }}</BaseButton></li>
      </ul>
      <div v-else-if="history.loading" class="grid flex-1 place-items-center"><BaseSpinner class="size-5" /></div>
      <BaseEmptyState v-else-if="!history.error" :title="filtering ? t('history.noMatches') : t('history.empty')" />
    </template>

    <BaseDialogConfirm
      v-model:open="clearOpen"
      :title="t('history.clearTitle')"
      :message="t('history.clearMessage', { name: profile?.name ?? '' })"
      :confirm-label="t('history.clear')"
      danger
      @confirm="clearAll"
    />
  </section>
</template>
