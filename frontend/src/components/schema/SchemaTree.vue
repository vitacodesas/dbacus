<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import { ChevronDown, ChevronRight, Columns3, Database, Eye, Folder, Hash, KeyRound, Link2, Plug, Table2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { selectStatement } from "../../api/schema";
import { asAppError } from "../../api/errors";
import { buildForest, type FlatRow, type RowKind, type SessionNode } from "../../composables/schemaTree";
import { useConnectionsStore } from "../../stores/connections";
import { useSchemaStore } from "../../stores/schema";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseContextMenu, { type MenuItem } from "../ui/BaseContextMenu.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import VirtualList from "../ui/VirtualList.vue";

const emit = defineEmits<{ openTable: [sessionId: string, db: string, table: string]; viewCreate: [sessionId: string, db: string, table: string]; refresh: [sessionId: string, db: string]; disconnect: [sessionId: string] }>();

const { t } = useI18n();
const schema = useSchemaStore();
const sessions = useSessionsStore();
const connections = useConnectionsStore();
const ui = useUiStore();

const ROW = 26;
const INDENT = 14;
const icons: Partial<Record<RowKind, typeof Database>> = {
  session: Plug, database: Database, group: Folder, table: Table2, view: Eye, section: Columns3, column: Columns3, index: Hash, fk: Link2,
};

/** Un nodo por sesión abierta; el contexto de cada una lee sus datos del store `schema`. */
const nodes = computed<SessionNode[]>(() =>
  sessions.list.map((s) => {
    const profile = connections.byId.get(s.profileId);
    const sid = s.sessionId;
    return {
      sessionId: sid,
      label: profile?.name ?? sid,
      hint: s.serverVersion,
      color: connections.colorOf(profile) || undefined,
      active: sid === sessions.activeSessionId,
      expanded: schema.isExpanded(sid, `s:${sid}`),
      ctx: {
        databases: schema.databasesOf(sid),
        tablesOf: (db) => schema.tablesOf(sid, db),
        detailOf: (db, tb) => schema.detailOf(sid, db, tb),
        isExpanded: (id) => schema.isExpanded(sid, id),
        isLoading: {
          dbs: schema.isLoading(schema.keyDbs(sid)),
          tables: (db) => schema.isLoading(schema.keyTables(sid, db)),
          detail: (db, tb) => schema.isLoading(schema.keyDetail(sid, db, tb)),
        },
        errorOf: {
          dbs: schema.errorOf(schema.keyDbs(sid)),
          tables: (db) => schema.errorOf(schema.keyTables(sid, db)),
          detail: (db, tb) => schema.errorOf(schema.keyDetail(sid, db, tb)),
        },
        filter: "",
        showSystem: schema.showSystem,
        activeDb: s.currentDatabase,
        t: (key, params) => t(key, params ?? {}),
      },
    };
  }),
);

const rows = computed(() => buildForest(nodes.value, schema.filter));

const list = ref<{ scrollToIndex: (i: number) => void; root: HTMLElement | null } | null>(null);
const focusedKey = ref<string | null>(null);
const tabbableKey = computed(() => (rows.value.find((r) => r.key === focusedKey.value) ?? rows.value[0])?.key ?? null);

function loaderFor(row: FlatRow): (() => Promise<void>) | undefined {
  if (row.kind === "session") return () => schema.loadDatabases(row.sessionId);
  if (row.kind === "database" && row.db) return () => schema.loadTables(row.sessionId, row.db!);
  if ((row.kind === "table" || row.kind === "view") && row.db && row.table) return () => schema.loadDetail(row.sessionId, row.db!, row.table!);
  return undefined;
}

/** Cualquier interacción dentro de una conexión la convierte en la activa. */
function activate(row: FlatRow) {
  focusedKey.value = row.key;
  sessions.setActive(row.sessionId);
}

async function toggle(row: FlatRow) {
  if (!row.expandable) return;
  // El id de nodo del store no lleva prefijo de sesión: la sesión va aparte en la clave.
  await schema.toggle(row.sessionId, row.id, loaderFor(row));
}

async function retry(row: FlatRow) {
  const r = row.retry;
  if (!r) return;
  if (r.level === "dbs") await schema.loadDatabases(row.sessionId);
  else if (r.level === "tables") await schema.loadTables(row.sessionId, r.db);
  else await schema.loadDetail(row.sessionId, r.db, r.table);
}

async function click(row: FlatRow) {
  activate(row);
  if (row.kind === "error") await retry(row);
  else await toggle(row);
}

function focusRow(index: number) {
  const target = rows.value[Math.max(0, Math.min(rows.value.length - 1, index))];
  if (!target) return;
  focusedKey.value = target.key;
  list.value?.scrollToIndex(rows.value.indexOf(target));
  void nextTick(() => list.value?.root?.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(target.key)}"]`)?.focus());
}

async function onKey(e: KeyboardEvent, row: FlatRow) {
  const i = rows.value.indexOf(row);
  switch (e.key) {
    case "ArrowDown": e.preventDefault(); focusRow(i + 1); break;
    case "ArrowUp": e.preventDefault(); focusRow(i - 1); break;
    case "Home": e.preventDefault(); focusRow(0); break;
    case "End": e.preventDefault(); focusRow(rows.value.length - 1); break;
    case "ArrowRight":
      e.preventDefault();
      if (row.expandable && !row.expanded) await toggle(row);
      else if (rows.value[i + 1]?.depth > row.depth) focusRow(i + 1);
      break;
    case "ArrowLeft":
      e.preventDefault();
      if (row.expandable && row.expanded) await toggle(row);
      else if (row.parentId) focusRow(rows.value.findIndex((r) => r.key === row.parentId));
      break;
    case "Enter":
    case " ":
      e.preventDefault();
      await click(row);
      break;
  }
}

function menuFor(row: FlatRow): MenuItem[] {
  if (row.kind === "session") {
    return [
      { id: "refresh", label: t("schema.menu.refresh") },
      { id: "disconnect", label: t("connections.actions.disconnect"), separatorBefore: true },
    ];
  }
  const isTable = row.kind === "table" || row.kind === "view";
  return [
    { id: "copyName", label: t("schema.menu.copyName") },
    ...(isTable ? [{ id: "copySelect", label: t("schema.menu.copySelect") }, { id: "viewCreate", label: t("schema.menu.viewCreate") }] : []),
    { id: "refresh", label: t("schema.menu.refresh"), separatorBefore: true },
  ];
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    ui.toast("success", t("schema.copied"));
  } catch {
    ui.toast("error", t("schema.copied") + " ✗");
  }
}

async function onMenu(row: FlatRow, id: string) {
  sessions.setActive(row.sessionId);
  try {
    if (id === "disconnect") emit("disconnect", row.sessionId);
    else if (id === "refresh") emit("refresh", row.sessionId, row.db ?? "");
    else if (!row.db) return;
    else if (id === "copyName") await copy(row.table ?? row.db);
    else if (id === "copySelect" && row.table) await copy(await selectStatement(row.sessionId, row.db, row.table));
    else if (id === "viewCreate" && row.table) emit("viewCreate", row.sessionId, row.db, row.table);
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}

const hasMenu = (row: FlatRow) => ["session", "database", "table", "view"].includes(row.kind);
</script>

<template>
  <VirtualList ref="list" :items="rows" :row-height="ROW" :item-key="(r) => r.key" class="h-full" role="tree" :aria-label="t('schema.title')">
    <template #default="{ item: row }">
      <component :is="hasMenu(row) ? BaseContextMenu : 'div'" v-bind="hasMenu(row) ? { items: menuFor(row), onSelect: (id: string) => onMenu(row, id) } : {}">
        <div
          :data-row-id="row.key"
          :data-kind="row.kind"
          :role="row.kind === 'loading' || row.kind === 'empty' || row.kind === 'error' ? 'none' : 'treeitem'"
          :aria-level="row.depth + 1"
          :aria-expanded="row.expandable ? row.expanded : undefined"
          :tabindex="row.key === tabbableKey ? 0 : -1"
          :title="row.title"
          class="flex cursor-default items-center gap-1 pr-2 text-sm select-none hover:bg-hover focus-visible:bg-hover"
          :class="[row.flags.system && 'text-muted', row.key === focusedKey && 'bg-hover', row.kind === 'session' && 'border-t border-border font-medium']"
          :style="{ height: `${ROW}px`, paddingLeft: `${row.depth * INDENT + 6}px` }"
          @click="click(row)"
          @dblclick="(row.kind === 'table' || row.kind === 'view') && row.db && row.table && emit('openTable', row.sessionId, row.db, row.table)"
          @keydown="onKey($event, row)"
          @focus="focusedKey = row.key"
        >
          <span class="grid size-4 shrink-0 place-items-center text-muted">
            <BaseSpinner v-if="row.kind === 'loading'" class="size-3" />
            <template v-else-if="row.expandable">
              <ChevronDown v-if="row.expanded" class="size-3.5" />
              <ChevronRight v-else class="size-3.5" />
            </template>
          </span>
          <span v-if="row.kind === 'session'" class="size-2.5 shrink-0 rounded-full bg-muted" :style="row.color ? { backgroundColor: row.color } : undefined" />
          <component :is="icons[row.kind]" v-else-if="icons[row.kind]" class="size-4 shrink-0" :class="row.kind === 'view' ? 'text-warning' : 'text-muted'" />
          <KeyRound v-if="row.flags.pk" class="size-3 shrink-0 text-warning" :aria-label="t('schema.flags.pk')" />
          <span
            class="truncate"
            :class="[row.kind === 'group' || row.kind === 'section' ? 'text-xs font-medium text-muted' : '', row.kind === 'error' && 'text-xs text-danger', row.kind === 'loading' || row.kind === 'empty' ? 'text-xs text-muted' : '', row.flags.active && row.kind !== 'session' && 'font-semibold', row.kind === 'session' && row.flags.active && 'text-accent']"
          >{{ row.label }}</span>
          <span v-if="row.hint" class="font-data truncate text-xs text-muted">{{ row.hint }}</span>
          <span v-if="row.flags.notNull && !row.flags.pk" class="shrink-0 text-[10px] text-muted" :title="t('schema.flags.notNull')">NN</span>
          <span v-if="row.flags.unique" class="shrink-0 text-[10px] text-muted" :title="t('schema.flags.unique')">UQ</span>
          <span v-if="row.flags.active" class="size-1.5 shrink-0 rounded-full bg-success" :title="t('schema.flags.active')" />
          <button v-if="row.kind === 'error'" type="button" class="ml-auto shrink-0 text-xs text-accent underline" @click.stop="retry(row)">{{ t("schema.retry") }}</button>
        </div>
      </component>
    </template>
  </VirtualList>
</template>
