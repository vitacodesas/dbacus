<script setup lang="ts">
import { useUiStore } from "../../stores/ui";
import ConnectionList from "../connections/ConnectionList.vue";
import HistoryPanel from "../history/HistoryPanel.vue";
import QueriesPanel from "../queries/QueriesPanel.vue";
import SchemaPanel from "../schema/SchemaPanel.vue";

const ui = useUiStore();
</script>

<template>
  <ConnectionList v-if="ui.sidebarView === 'connections'" />
  <SchemaPanel v-else-if="ui.sidebarView === 'schema'" />
  <QueriesPanel v-else-if="ui.sidebarView === 'queries'" />
  <HistoryPanel v-else />
</template>
