<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ChevronDown, ChevronRight, FileCode, Folder, FolderOpen, FolderPlus, FolderSearch, RefreshCw, Search, ArrowDownAZ, Clock } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useConnectionsStore } from "../../stores/connections";
import { useQueryFilesStore } from "../../stores/queryFiles";
import { useQueryTabsStore } from "../../stores/queryTabs";
import type { QueryNode } from "../../types";
import { relativeTime } from "../../utils/time";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseContextMenu, { type MenuItem } from "../ui/BaseContextMenu.vue";
import BaseDialogConfirm from "../ui/BaseDialogConfirm.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import VirtualList from "../ui/VirtualList.vue";
import { modKey } from "../../composables/useShortcuts";

const { t, locale } = useI18n();
const files = useQueryFilesStore();
const tabs = useQueryTabsStore();
const connections = useConnectionsStore();

const ROW = 28;
const INDENT = 14;

onMounted(async () => {
  await files.load();
  await files.syncTabs();
});

// Búsqueda con debounce: por nombre, y por contenido si se marca (todo en Go).
const text = ref(files.query);
const content = ref(files.searchContent);
let timer: ReturnType<typeof setTimeout> | null = null;
watch([text, content], () => {
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => void files.search(text.value, content.value), 250);
});
onBeforeUnmount(() => timer && clearTimeout(timer));

const profileOf = (id: string) => connections.profiles.find((p) => p.id === id);
const colorOf = (n: QueryNode) => {
  const p = n.profileId ? profileOf(n.profileId) : undefined;
  return p ? connections.colorOf(p) : "";
};
const tooltip = (n: QueryNode) =>
  n.kind === "folder" ? n.path : `${n.path}${n.profileName ? `\n${n.profileName}${n.database ? ` · ${n.database}` : ""}` : ""}\n${relativeTime(n.modifiedAt, locale.value)}`;

function menuFor(n: QueryNode): MenuItem[] {
  const common: MenuItem[] = [
    { id: "rename", label: t("queries.rename") },
    { id: "move", label: t("queries.moveTo") },
  ];
  if (n.kind === "folder") return [{ id: "newFolder", label: t("queries.newFolderInside") }, ...common, { id: "reveal", label: t("queries.reveal"), separatorBefore: true }, { id: "delete", label: t("queries.delete"), danger: true, separatorBefore: true }];
  return [
    { id: "open", label: t("queries.open") }, ...common, { id: "duplicate", label: t("queries.duplicate") },
    { id: "reveal", label: t("queries.reveal"), separatorBefore: true }, { id: "delete", label: t("queries.delete"), danger: true, separatorBefore: true },
  ];
}
function onMenu(n: QueryNode, id: string) {
  if (id === "open") void files.openFile(n.path);
  else if (id === "rename" || id === "move") files.openEntryDialog(id, n.path);
  else if (id === "newFolder") files.openEntryDialog("newFolder", n.path);
  else if (id === "duplicate") void files.duplicate(n.path);
  else if (id === "reveal") void files.reveal(n.path);
  else if (id === "delete") files.askDelete(n.path);
}
function activate(n: QueryNode) {
  if (n.kind === "folder") files.toggle(n.path);
  else void files.openFile(n.path);
}
// BaseDialogConfirm cierra (v-model) antes de emitir `confirm`: se guarda lo que se iba a borrar para no perderlo.
let pendingDelete: typeof files.deletePrompt = null;
const deleteOpen = computed({
  get: () => !!files.deletePrompt,
  set: (v) => {
    if (!v) {
      pendingDelete = files.deletePrompt;
      files.deletePrompt = null;
    }
  },
});

const shortPath = computed(() => {
  const p = files.root?.path ?? "";
  return p.length > 38 ? `…${p.slice(-37)}` : p;
});
const searching = computed(() => files.hits !== null || !!text.value.trim());
</script>

<template>
  <section class="flex h-full min-h-0 flex-col" :aria-label="t('queries.title')" data-testid="queries-panel">
    <header class="flex items-center justify-between px-3 py-2">
      <h2 class="truncate text-xs font-semibold tracking-wide text-muted uppercase">{{ t("queries.title") }}</h2>
      <div class="flex items-center gap-0.5">
        <BaseButton size="sm" variant="ghost" :title="files.sort === 'name' ? t('queries.sortModified') : t('queries.sortName')" :aria-label="files.sort === 'name' ? t('queries.sortModified') : t('queries.sortName')" data-testid="queries-sort" @click="files.sort = files.sort === 'name' ? 'modified' : 'name'">
          <ArrowDownAZ v-if="files.sort === 'name'" class="size-4" /><Clock v-else class="size-4" />
        </BaseButton>
        <BaseButton size="sm" variant="ghost" :title="t('queries.newFolder')" :aria-label="t('queries.newFolder')" data-testid="queries-new-folder" @click="files.openEntryDialog('newFolder', '')"><FolderPlus class="size-4" /></BaseButton>
        <BaseButton size="sm" variant="ghost" :title="t('queries.refresh')" :aria-label="t('queries.refresh')" data-testid="queries-refresh" @click="files.load().then(files.syncTabs)"><RefreshCw class="size-4" :class="files.loading && 'animate-spin'" /></BaseButton>
      </div>
    </header>

    <div class="flex flex-col gap-1.5 px-3 pb-2">
      <label class="relative block">
        <Search class="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted" />
        <input v-model="text" type="search" class="w-full rounded-md border border-border bg-bg py-1 pr-2 pl-7 text-sm" :placeholder="t('queries.search')" :aria-label="t('queries.search')" data-testid="queries-search">
      </label>
      <BaseCheckbox v-model="content" class="text-xs text-muted" data-testid="queries-search-content">{{ t("queries.searchContent") }}</BaseCheckbox>
    </div>

    <!-- Lo que está abierto y aún no es un archivo (o cambió): lo que antes «se perdía» -->
    <div v-if="tabs.unsaved.length && !searching" class="mx-3 mb-2 rounded-md border border-warning/40 bg-warning/10 p-1.5" data-testid="queries-unsaved">
      <h3 class="px-1 pb-1 text-[11px] font-semibold text-muted uppercase">{{ t("queries.unsaved") }}</h3>
      <ul>
        <li v-for="tab in tabs.unsaved" :key="tab.id" class="group flex items-center gap-1 rounded px-1 py-0.5 text-sm hover:bg-hover">
          <button class="min-w-0 flex-1 truncate text-left" :title="tab.filePath || tab.title" @click="tabs.setActive(tab.id)">● {{ tab.title }}</button>
          <button class="rounded px-1.5 text-xs text-accent hover:bg-hover" :aria-label="`${t('common.save')}: ${tab.title}`" @click="files.save(tab.id)">{{ t("common.save") }}</button>
        </li>
      </ul>
    </div>

    <div v-if="files.error && !files.root?.exists" class="mx-3 rounded-md border border-danger p-2 text-sm" role="alert" data-testid="queries-root-error">
      <p>{{ t("queries.rootError") }}</p>
      <p v-if="files.root?.path" class="font-data mt-1 text-xs break-all text-muted">{{ files.root.path }}</p>
      <div class="mt-2 flex gap-2"><BaseButton size="sm" @click="files.pickRoot()">{{ t("queries.chooseOther") }}</BaseButton><BaseButton size="sm" @click="files.load()">{{ t("connections.retry") }}</BaseButton></div>
    </div>

    <template v-else>
      <!-- Resultados de búsqueda -->
      <ul v-if="searching" class="min-h-0 flex-1 overflow-auto px-2 pb-2" data-testid="queries-hits">
        <li v-if="files.searching" class="flex items-center gap-2 px-2 py-1 text-xs text-muted"><BaseSpinner class="size-3" />{{ t("queries.searching") }}</li>
        <li v-else-if="!files.hits?.length" class="px-2 py-1 text-xs text-muted">{{ t("queries.noMatches") }}</li>
        <li v-for="h in files.hits ?? []" :key="h.path" class="rounded-md px-2 py-1 hover:bg-hover">
          <button class="block w-full text-left" :title="h.path" data-testid="queries-hit" @click="files.openFile(h.path)">
            <span class="flex items-center gap-1.5 text-sm"><FileCode class="size-3.5 shrink-0 text-muted" /><span class="truncate">{{ h.name }}</span></span>
            <span class="block truncate pl-5 text-[11px] text-muted">{{ h.path }}</span>
            <span v-if="h.line" class="font-data block truncate pl-5 text-xs"><span class="text-muted">{{ h.line }}:</span> {{ h.snippet }}</span>
          </button>
        </li>
      </ul>

      <BaseEmptyState v-else-if="files.tree && !files.nodes.length && !files.loading" :title="t('queries.empty')" :description="t('queries.emptyHint', { key: `${modKey}S` })" data-testid="queries-empty">
        <template #icon><FileCode class="size-8" /></template>
      </BaseEmptyState>

      <VirtualList v-else :items="files.rows" :row-height="ROW" :item-key="(r) => r.node.path" class="min-h-0 flex-1" role="tree" :aria-label="t('queries.title')" data-testid="queries-tree">
        <template #default="{ item: row }">
          <BaseContextMenu :items="menuFor(row.node)" @select="(id: string) => onMenu(row.node, id)">
            <div
              role="treeitem"
              tabindex="0"
              :aria-level="row.depth + 1"
              :aria-expanded="row.node.kind === 'folder' ? !files.collapsed.has(row.node.path) : undefined"
              :data-kind="row.node.kind"
              :data-path="row.node.path"
              :title="tooltip(row.node)"
              class="flex cursor-default items-center gap-1 pr-2 text-sm select-none hover:bg-hover focus-visible:bg-hover"
              :style="{ height: `${ROW}px`, paddingLeft: `${row.depth * INDENT + 8}px` }"
              @click="row.node.kind === 'folder' && activate(row.node)"
              @dblclick="row.node.kind === 'file' && activate(row.node)"
              @keydown.enter.prevent="activate(row.node)"
            >
              <template v-if="row.node.kind === 'folder'">
                <component :is="files.collapsed.has(row.node.path) ? ChevronRight : ChevronDown" class="size-3.5 shrink-0 text-muted" />
                <component :is="files.collapsed.has(row.node.path) ? Folder : FolderOpen" class="size-4 shrink-0 text-muted" />
              </template>
              <FileCode v-else class="ml-[18px] size-4 shrink-0 text-muted" />
              <span class="truncate">{{ row.node.name }}</span>
              <span v-if="row.node.kind === 'file' && row.node.profileId" class="ml-auto flex shrink-0 items-center gap-1 text-[11px] text-muted">
                <span class="size-2 rounded-full" :style="{ background: colorOf(row.node) || 'var(--border)' }" />
                <span v-if="row.node.database" class="font-data max-w-20 truncate">{{ row.node.database }}</span>
              </span>
            </div>
          </BaseContextMenu>
        </template>
      </VirtualList>
    </template>

    <p v-if="files.tree?.truncated" class="px-3 py-1 text-[11px] text-warning" role="status">{{ t("queries.truncated") }}</p>

    <!-- La respuesta a «¿dónde quedó?»: la ruta, siempre a la vista, y un clic la abre en el explorador -->
    <footer class="border-t border-border px-3 py-2">
      <button v-if="files.root?.path" class="flex w-full items-center gap-1.5 text-left text-[11px] text-muted hover:text-fg" :title="`${files.root.path}\n${t('queries.reveal')}`" data-testid="queries-root" @click="files.reveal('')">
        <FolderSearch class="size-3.5 shrink-0" /><span class="font-data truncate">{{ shortPath }}</span>
      </button>
    </footer>

    <BaseDialogConfirm
      v-model:open="deleteOpen"
      :title="t('queries.deleteTitle')"
      :message="t(files.deletePrompt?.isFolder ? 'queries.deleteFolderMessage' : 'queries.deleteMessage', { path: files.deletePrompt?.path ?? '' })"
      :confirm-label="t('queries.delete')"
      danger
      @confirm="files.confirmDelete(pendingDelete)"
    />
  </section>
</template>
