<script setup lang="ts">
import { computed, ref } from "vue";
import { Database, Plus } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { asAppError } from "../../api/errors";
import { useConnectFlow } from "../../composables/useConnectFlow";
import { useConnectionsStore } from "../../stores/connections";
import { useConnectionUiStore } from "../../stores/connectionUi";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import ConnectionCard from "./ConnectionCard.vue";

const { t } = useI18n();
const connections = useConnectionsStore();
const sessions = useSessionsStore();
const uiConn = useConnectionUiStore();
const ui = useUiStore();
const { connect } = useConnectFlow();

const query = ref("");
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase();
  if (!q) return connections.profiles;
  return connections.profiles.filter((p) => [p.name, p.host, p.database, p.user].some((v) => v?.toLowerCase().includes(q)));
});

function selectConnected(profileId: string) {
  const s = sessions.sessionFor(profileId);
  if (s) sessions.setActive(s.sessionId);
}

async function disconnect(profileId: string) {
  const s = sessions.sessionFor(profileId);
  if (!s) return;
  try {
    await sessions.disconnect(s.sessionId);
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}

async function clone(id: string) {
  try {
    await connections.clone(id);
    ui.toast("success", t("connections.toast.cloned"));
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}
</script>

<template>
  <section class="flex h-full flex-col" :aria-label="t('connections.title')">
    <div class="flex items-center justify-between px-3 py-2">
      <h2 class="text-xs font-semibold tracking-wide text-muted uppercase">{{ t("connections.title") }}</h2>
      <BaseButton size="sm" variant="ghost" :aria-label="t('connections.new')" :title="t('connections.new')" @click="uiConn.openNew()">
        <Plus class="size-4" />
      </BaseButton>
    </div>

    <div v-if="connections.loading && !connections.loaded" class="grid flex-1 place-items-center"><BaseSpinner /></div>

    <div v-else-if="connections.lastError" class="flex flex-col items-center gap-2 p-4 text-center text-sm">
      <p class="text-danger">{{ t("connections.loadError") }}</p>
      <p class="text-xs text-muted">{{ connections.lastError.message }}</p>
      <BaseButton size="sm" @click="connections.load()">{{ t("connections.retry") }}</BaseButton>
    </div>

    <BaseEmptyState v-else-if="!connections.profiles.length" :title="t('connections.empty.title')" :description="t('connections.empty.description')">
      <template #icon><Database class="size-8" /></template>
      <BaseButton variant="primary" @click="uiConn.openNew()">{{ t("connections.new") }}</BaseButton>
    </BaseEmptyState>

    <template v-else>
      <div class="px-3 pb-2">
        <input
          v-model="query"
          type="search"
          :placeholder="t('connections.search')"
          :aria-label="t('connections.search')"
          class="h-8 w-full rounded-md border border-border bg-bg px-2.5 text-sm placeholder:text-muted/70"
        >
      </div>
      <ul class="flex-1 overflow-auto px-2 pb-2" role="list">
        <ConnectionCard
          v-for="p in filtered"
          :key="p.id"
          :profile="p"
          :connected="sessions.connectedProfileIds.has(p.id)"
          :connecting="sessions.isConnecting(p.id)"
          @select="selectConnected(p.id)"
          @connect="connect(p.id)"
          @cancel="sessions.cancelConnect(p.id)"
          @disconnect="disconnect(p.id)"
          @edit="uiConn.openEdit(p.id)"
          @clone="clone(p.id)"
          @delete="uiConn.deleteTargetId = p.id"
        />
        <li v-if="!filtered.length" class="p-4 text-center text-xs text-muted">{{ t("connections.noMatches") }}</li>
      </ul>
    </template>
  </section>
</template>
