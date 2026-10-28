<script setup lang="ts">
import { Database } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import BaseButton from "../components/ui/BaseButton.vue";
import BaseEmptyState from "../components/ui/BaseEmptyState.vue";
import { useQueryActions } from "../composables/useQueryActions";
import { useConnectionUiStore } from "../stores/connectionUi";
import { useSessionsStore } from "../stores/sessions";

const { t } = useI18n();
const uiConn = useConnectionUiStore();
const sessions = useSessionsStore();
const { newQuery } = useQueryActions();
</script>

<template>
  <div class="grid h-full place-items-center">
    <BaseEmptyState :title="t('welcome.title')" :description="t('welcome.description')">
      <template #icon><Database class="size-10" /></template>
      <BaseButton v-if="sessions.list.length" variant="primary" @click="newQuery">{{ t("query.newTab") }}</BaseButton>
      <BaseButton :variant="sessions.list.length ? 'secondary' : 'primary'" @click="uiConn.openNew()">{{ t("welcome.newConnection") }}</BaseButton>
    </BaseEmptyState>
  </div>
</template>
