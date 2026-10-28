<script setup lang="ts">
import { computed } from "vue";
import { Lock, Pencil, Plug, Unplug } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import type { ConnectionProfile } from "../../types";
import BaseContextMenu, { type MenuItem } from "../ui/BaseContextMenu.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const props = defineProps<{ profile: ConnectionProfile; connected: boolean; connecting: boolean }>();
const emit = defineEmits<{
  select: [];
  connect: [];
  disconnect: [];
  cancel: [];
  edit: [];
  clone: [];
  delete: [];
}>();
const { t } = useI18n();

const subtitle = computed(() => {
  const p = props.profile;
  return `${p.user}@${p.host}:${p.port}${p.database ? `/${p.database}` : ""}`;
});
const status = computed(() => (props.connecting ? "connecting" : props.connected ? "connected" : "disconnected"));

const menu = computed<MenuItem[]>(() => [
  props.connecting
    ? { id: "cancel", label: t("connections.actions.cancel") }
    : props.connected
      ? { id: "disconnect", label: t("connections.actions.disconnect") }
      : { id: "connect", label: t("connections.actions.connect") },
  { id: "edit", label: t("connections.actions.edit"), separatorBefore: true },
  { id: "clone", label: t("connections.actions.clone") },
  { id: "delete", label: t("connections.actions.delete"), danger: true, separatorBefore: true },
]);

function onMenu(id: string) {
  const handlers: Record<string, () => void> = {
    connect: () => emit("connect"), disconnect: () => emit("disconnect"), cancel: () => emit("cancel"),
    edit: () => emit("edit"), clone: () => emit("clone"), delete: () => emit("delete"),
  };
  handlers[id]?.();
}

function toggle() {
  if (props.connecting) emit("cancel");
  else if (props.connected) emit("disconnect");
  else emit("connect");
}
</script>

<template>
  <BaseContextMenu :items="menu" @select="onMenu">
    <li
      class="group relative flex items-center gap-2 rounded-md border-l-[3px] px-2 py-1.5 hover:bg-hover focus-within:bg-hover"
      :style="{ borderLeftColor: profile.color || 'transparent' }"
      :data-testid="`connection-${profile.name}`"
      @dblclick="!connected && !connecting && emit('connect')"
    >
      <span class="grid size-4 shrink-0 place-items-center" :title="t(`connections.status.${status}`)">
        <BaseSpinner v-if="connecting" class="size-3.5" />
        <span v-else class="size-2 rounded-full" :class="connected ? 'bg-success' : 'bg-border'" />
        <span class="sr-only">{{ t(`connections.status.${status}`) }}</span>
      </span>
      <button
        type="button"
        class="min-w-0 flex-1 text-left"
        @click="connected && emit('select')"
        @keydown.enter.prevent="connected ? emit('select') : !connecting && emit('connect')"
      >
        <span class="flex items-center gap-1 text-sm font-medium">
          <span class="truncate">{{ profile.name }}</span>
          <Lock v-if="profile.readOnly" class="size-3 shrink-0 text-warning" :aria-label="t('status.readOnly')" :title="t('status.readOnly')" data-testid="card-readonly" />
        </span>
        <span class="font-data block truncate text-xs text-muted">{{ subtitle }}</span>
      </button>
      <span class="flex shrink-0 gap-0.5 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
        <button
          type="button"
          class="grid size-6 place-items-center rounded text-muted hover:bg-border hover:text-fg"
          :aria-label="connecting ? t('connections.actions.cancel') : connected ? t('connections.actions.disconnect') : t('connections.actions.connect')"
          @click="toggle"
        >
          <Unplug v-if="connected || connecting" class="size-3.5" />
          <Plug v-else class="size-3.5" />
        </button>
        <button type="button" class="grid size-6 place-items-center rounded text-muted hover:bg-border hover:text-fg" :aria-label="t('connections.actions.edit')" @click="emit('edit')">
          <Pencil class="size-3.5" />
        </button>
      </span>
    </li>
  </BaseContextMenu>
</template>
