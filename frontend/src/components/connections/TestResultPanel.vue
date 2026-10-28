<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import { Check, CircleDashed, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { ErrorCode } from "../../api/errors";
import type { TestResult } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const emit = defineEmits<{ cancel: []; trust: [fingerprint: string] }>();
const { t } = useI18n();

// El panel aparece bajo el formulario; asegura que el resultado quede a la vista dentro del modal con scroll.
const root = ref<HTMLElement | null>(null);
const props = defineProps<{ running: boolean; result: TestResult | null }>();
watch(() => [props.running, props.result], () => nextTick(() => root.value?.scrollIntoView({ block: "nearest" })));
</script>

<template>
  <section v-if="running || result" ref="root" class="rounded-md border border-border bg-bg p-3 text-sm" aria-live="polite">
    <div v-if="running" class="flex items-center gap-2">
      <BaseSpinner />
      <span>{{ t("test.running") }}</span>
      <BaseButton size="sm" class="ml-auto" @click="emit('cancel')">{{ t("test.cancel") }}</BaseButton>
    </div>
    <template v-else-if="result">
      <p class="mb-2 font-medium" :class="result.ok ? 'text-success' : 'text-danger'">{{ result.ok ? t("test.ok") : t("test.failed") }}</p>
      <ul class="flex flex-col gap-1">
        <li v-for="s in result.steps ?? []" :key="s.name" class="flex items-start gap-2" :data-step="s.name">
          <Check v-if="s.ok" class="mt-0.5 size-4 shrink-0 text-success" />
          <X v-else-if="!s.skipped" class="mt-0.5 size-4 shrink-0 text-danger" />
          <CircleDashed v-else class="mt-0.5 size-4 shrink-0 text-muted" />
          <div class="min-w-0 flex-1" :class="s.skipped && 'text-muted'">
            <span>{{ t(`test.steps.${s.name}`) }}</span>
            <span v-if="s.skipped" class="ml-1 text-xs">({{ t("test.skipped") }})</span>
            <span v-else class="ml-1 text-xs text-muted">{{ s.elapsedMs }} ms</span>
            <p v-if="s.error" class="mt-0.5 text-xs text-danger">{{ s.error.message }}</p>
            <p v-if="s.error?.detail && s.error.code !== 'SSH_HOSTKEY_UNKNOWN'" class="font-data mt-0.5 text-xs break-all text-muted">{{ s.error.detail }}</p>
            <BaseButton
              v-if="s.error?.code === ErrorCode.SSHHostKeyUnknown"
              size="sm"
              class="mt-1"
              @click="emit('trust', s.error.detail ?? '')"
            >
              {{ t("test.trust") }}
            </BaseButton>
          </div>
        </li>
      </ul>
      <p v-if="result.ok" class="mt-2 text-xs text-muted">
        {{ t("test.version", { version: result.serverVersion }) }} · {{ t("test.latency", { ms: result.latencyMs }) }}
      </p>
    </template>
  </section>
</template>
