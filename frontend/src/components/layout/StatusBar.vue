<script setup lang="ts">
import { computed } from "vue";
import { Lock, LockOpen } from "lucide-vue-next";
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
    <!-- Firma de la organización (spec 17): esquina inferior izquierda, siempre visible aunque se colapse la barra lateral. -->
    <span class="shrink-0 border-r border-border pr-3 select-none" data-testid="signature">{{ t("app.signature") }}</span>
    <template v-if="sessions.active">
      <span class="flex min-w-0 items-center gap-1.5 text-fg" data-testid="status-session">
        <span class="size-2 shrink-0 rounded-full bg-success" /><span class="truncate">{{ profile?.name }}</span>
      </span>
      <span class="font-data">{{ sessions.active.serverVersion }}</span>
      <span v-if="sessions.active.currentDatabase" class="font-data">{{ sessions.active.currentDatabase }}</span>
      <span v-if="profile?.readOnly" class="flex items-center gap-1 text-warning" data-testid="status-readonly" :title="t('status.readOnlyHint')"><Lock class="size-3" />{{ t("status.readOnly") }}</span>
      <span v-else class="flex items-center gap-1" data-testid="status-readwrite" :title="t('status.readWriteHint')"><LockOpen class="size-3" />{{ t("status.readWrite") }}</span>
    </template>
    <span v-else>{{ t("status.noSession") }}</span>
    <span class="ml-auto">{{ t("status.ready") }}</span>
  </footer>
</template>
