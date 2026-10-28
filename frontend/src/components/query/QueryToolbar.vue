<script setup lang="ts">
import { computed, watch } from "vue";
import { AlignLeft, Lock, Play, TextCursorInput, Square } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { modKey } from "../../composables/useShortcuts";
import { useConnectionsStore } from "../../stores/connections";
import { useQueryTabsStore, type QueryTab } from "../../stores/queryTabs";
import { useResultsStore } from "../../stores/results";
import { useSchemaStore } from "../../stores/schema";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";

const props = defineProps<{ tab: QueryTab }>();
const emit = defineEmits<{ run: []; runSelection: []; stop: []; format: [] }>();

const { t } = useI18n();
const tabs = useQueryTabsStore();
const connections = useConnectionsStore();
const sessions = useSessionsStore();
const schema = useSchemaStore();
const results = useResultsStore();
const ui = useUiStore();

const profile = computed(() => connections.byId.get(props.tab.profileId));
const session = computed(() => sessions.sessionFor(props.tab.profileId));
const running = computed(() => results.running(props.tab.id));

// Bases disponibles: se piden a Go al tener sesión (no dependen de que el panel de esquema se haya abierto); las del
// sistema van aparte para no mezclarlas con las del usuario.
watch(() => session.value?.sessionId, (sid) => sid && void schema.loadDatabases(sid), { immediate: true });
const allDatabases = computed(() => (session.value ? (schema.databasesOf(session.value.sessionId) ?? []) : []));
const userDbs = computed(() => allDatabases.value.filter((d) => !d.isSystem).map((d) => d.name));
const systemDbs = computed(() => allDatabases.value.filter((d) => d.isSystem).map((d) => d.name));
// La base elegida en la pestaña siempre aparece, aunque aún no haya llegado la lista.
const extraDb = computed(() => (props.tab.database && ![...userDbs.value, ...systemDbs.value].includes(props.tab.database) ? [props.tab.database] : []));

const limits = computed(() => [100, 500, 1000, 0].map((v) => ({ value: v, label: v === 0 ? t("query.limitNone") : String(v) })));
const key = (combo: string) => `${modKey}${combo}`;
</script>

<template>
  <div class="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-panel px-2" role="toolbar" :aria-label="t('query.run')">
    <BaseButton v-if="!running" variant="primary" size="sm" :title="`${t('query.run')} (${key('↵')})`" :aria-label="t('query.run')" data-testid="run-all" @click="emit('run')">
      <Play class="size-3.5" /> {{ t("query.run") }}
    </BaseButton>
    <BaseButton v-else variant="danger" size="sm" :title="t('query.stop')" :aria-label="t('query.stop')" data-testid="stop" @click="emit('stop')">
      <Square class="size-3.5" /> {{ t("query.stop") }}
    </BaseButton>
    <BaseButton size="sm" :disabled="running" :title="`${t('query.runSelection')} (${key('⇧↵')})`" :aria-label="t('query.runSelection')" data-testid="run-selection" @click="emit('runSelection')">
      <TextCursorInput class="size-3.5" />
    </BaseButton>
    <BaseButton size="sm" variant="ghost" :title="`${t('query.format')} (${key('⌥L')})`" :aria-label="t('query.format')" data-testid="format" @click="emit('format')">
      <AlignLeft class="size-3.5" />
    </BaseButton>

    <span class="mx-1 h-5 w-px bg-border" />
    <span class="flex min-w-0 items-center gap-1.5 text-xs" :title="session ? '' : t('query.connectToRun')">
      <span class="size-2 shrink-0 rounded-full" :class="session ? 'bg-success' : 'bg-border'" :style="profile?.color && session ? { backgroundColor: profile.color } : undefined" />
      <span class="truncate" data-testid="toolbar-connection">{{ profile?.name ?? t("query.noConnection") }}</span>
      <span v-if="profile?.readOnly" class="flex shrink-0 items-center gap-1 rounded bg-warning/15 px-1.5 py-0.5 text-warning" data-testid="toolbar-readonly" :title="t('status.readOnlyHint')">
        <Lock class="size-3" />{{ t("status.readOnly") }}
      </span>
    </span>

    <label class="ml-2 flex items-center gap-1 text-xs whitespace-nowrap text-muted">
      {{ t("query.database") }}
      <select
        class="h-7 max-w-40 rounded-md border border-border bg-panel px-1.5 text-xs text-fg"
        :value="tab.database"
        :disabled="!session"
        data-testid="database-select"
        @change="tabs.setDatabase(tab.id, ($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ session?.currentDatabase ? t("query.defaultDatabase", { db: session.currentDatabase }) : t("query.noDatabase") }}</option>
        <option v-for="d in extraDb" :key="d" :value="d">{{ d }}</option>
        <option v-for="d in userDbs" :key="d" :value="d">{{ d }}</option>
        <optgroup v-if="systemDbs.length" :label="t('query.systemDatabases')">
          <option v-for="d in systemDbs" :key="d" :value="d">{{ d }}</option>
        </optgroup>
      </select>
    </label>

    <label class="ml-auto flex items-center gap-1 text-xs whitespace-nowrap text-muted">
      {{ t("query.limit") }}
      <select v-model.number="ui.autoLimit" class="h-7 rounded-md border border-border bg-panel px-1.5 text-xs text-fg" data-testid="limit-select">
        <option v-for="o in limits" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
    </label>
  </div>
</template>
