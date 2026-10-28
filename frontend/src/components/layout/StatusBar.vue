<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useConnectionsStore } from "../../stores/connections";
import { useSessionsStore } from "../../stores/sessions";

const { t } = useI18n();
const sessions = useSessionsStore();
const connections = useConnectionsStore();
const profile = computed(() => (sessions.active ? connections.byId.get(sessions.active.profileId) : undefined));
</script>

<template>
  <footer class="flex h-6 shrink-0 items-center gap-4 border-t border-border bg-panel px-3 text-xs text-muted">
    <template v-if="sessions.active">
      <span class="flex items-center gap-1.5 text-fg" data-testid="status-session">
        <span class="size-2 rounded-full bg-success" />{{ profile?.name }}
      </span>
      <span class="font-data">{{ sessions.active.serverVersion }}</span>
      <span v-if="sessions.active.currentDatabase" class="font-data">{{ sessions.active.currentDatabase }}</span>
    </template>
    <span v-else>{{ t("status.noSession") }}</span>
    <span class="ml-auto">{{ t("status.ready") }}</span>
  </footer>
</template>
