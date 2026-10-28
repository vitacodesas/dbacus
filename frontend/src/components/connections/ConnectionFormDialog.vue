<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { noSecrets, parseDsn, startTest, trustHostKey, type Cancellable } from "../../api/connections";
import { AppException, ErrorCode, asAppError } from "../../api/errors";
import {
  applyParsed, emptyForm, firstTabWithError, formFromProfile, toProfile, toSecrets, validateForm,
  type FieldErrors, type FormState, type FormTab,
} from "../../composables/connectionForm";
import { useConnectFlow } from "../../composables/useConnectFlow";
import { useConnectionsStore } from "../../stores/connections";
import { useConnectionUiStore } from "../../stores/connectionUi";
import { useUiStore } from "../../stores/ui";
import type { ConnectionSecrets, TestResult } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";
import BaseTabs from "../ui/BaseTabs.vue";
import BaseToggle from "../ui/BaseToggle.vue";
import PasswordField from "./PasswordField.vue";
import PathField from "./PathField.vue";
import TestResultPanel from "./TestResultPanel.vue";

const { t } = useI18n();
const connections = useConnectionsStore();
const ui = useUiStore();
const uiConn = useConnectionUiStore();
const { connect } = useConnectFlow();

const COLORS = ["#ef4444", "#f97316", "#eab308", "#22c55e", "#06b6d4", "#3b82f6", "#8b5cf6", "#ec4899"];
const PARAM_SUGGESTIONS = ["charset", "time_zone", "sql_mode", "timeout", "readTimeout", "writeTimeout", "wait_timeout"];

const open = computed({ get: () => uiConn.formOpen, set: (v) => (v ? uiConn.openNew() : uiConn.closeForm()) });
const isEdit = computed(() => !!uiConn.editingId);

const form = reactive<FormState>(emptyForm());
const errors = ref<FieldErrors>({});
const tab = ref<FormTab>("general");
const saving = ref(false);
const testing = ref<Cancellable<TestResult> | null>(null);
const testResult = ref<TestResult | null>(null);
const dsnText = ref("");
const dsnInfo = ref("");

const driverOptions = computed(() => [
  ...connections.drivers.map((d) => ({ value: d.name, label: d.displayName })),
  ...["PostgreSQL", "SQLite"].map((n) => ({ value: n.toLowerCase(), label: `${n} (${t("form.drivers.soon")})`, disabled: true })),
]);
const tlsOptions = computed(() => ["disable", "preferred", "required", "verify-ca", "verify-full"].map((m) => ({ value: m, label: t(`form.tlsModes.${m}`) })));
const sshMethodOptions = computed(() => ["password", "key"].map((m) => ({ value: m, label: t(`form.sshMethods.${m}`) })));
const tabs = computed(() => (["general", "tls", "ssh", "advanced", "import"] as FormTab[]).map((v) => ({ value: v, label: t(`form.tabs.${v}`) })));

watch(open, (isOpen) => {
  if (!isOpen) return;
  const existing = uiConn.editingId ? connections.byId.get(uiConn.editingId) : undefined;
  const d = connections.drivers[0];
  Object.assign(form, existing ? formFromProfile(existing) : emptyForm(d?.name ?? "mysql", d?.defaultPort ?? 3306));
  errors.value = {};
  tab.value = "general";
  testResult.value = null;
  dsnText.value = "";
  dsnInfo.value = "";
});

function showErrors(e: FieldErrors) {
  errors.value = e;
  const target = firstTabWithError(e);
  if (target) tab.value = target;
}

/** Valida en el cliente; Go repite la validación y es la autoridad. */
function validate(): boolean {
  const e = validateForm(form, t);
  showErrors(e);
  return Object.keys(e).length === 0;
}

function applyServerError(e: unknown): boolean {
  const err = asAppError(e);
  if (err.code === ErrorCode.Validation && err.detail) {
    showErrors({ [err.detail]: err.message });
    return true;
  }
  return false;
}

async function runTest() {
  if (!validate()) return;
  testResult.value = null;
  const run = startTest(toProfile(form), toSecrets(form));
  testing.value = run;
  try {
    testResult.value = await run.result;
  } catch (e) {
    const err = asAppError(e);
    if (err.code === ErrorCode.QueryCanceled) return;
    if (!applyServerError(e)) ui.toast("error", err.message);
  } finally {
    testing.value = null;
  }
}

async function trustAndRetest(fingerprint: string) {
  try {
    await trustHostKey(form.ssh.host.trim(), Number(form.ssh.port) || 22, fingerprint);
  } catch (e) {
    ui.toast("error", asAppError(e).message);
    return;
  }
  await runTest();
}

async function save(thenConnect = false) {
  if (saving.value || !validate()) return;
  saving.value = true;
  try {
    const secrets = toSecrets(form);
    const saved = await connections.save(toProfile(form), secrets);
    ui.toast("success", t("connections.toast.saved"));
    // Si la contraseña no se guarda, lo escrito aquí se reutiliza para conectar sin volver a pedirlo.
    const overrides = !form.savePassword ? ephemeral(secrets) : null;
    uiConn.closeForm();
    if (thenConnect) void connect(saved.id, overrides);
  } catch (e) {
    if (!applyServerError(e)) ui.toast("error", asAppError(e).message);
  } finally {
    saving.value = false;
  }
}

function ephemeral(s: ConnectionSecrets): ConnectionSecrets | null {
  const o: ConnectionSecrets = { ...noSecrets(), password: s.password, sshPassword: s.sshPassword, sshKeyPassphrase: s.sshKeyPassphrase };
  return o.password !== null || o.sshPassword !== null || o.sshKeyPassphrase !== null ? o : null;
}

async function importDsn() {
  dsnInfo.value = "";
  delete errors.value.dsn;
  try {
    const parsed = await parseDsn(dsnText.value);
    applyParsed(form, parsed.profile, parsed.secrets.password);
    dsnInfo.value = t("form.import.applied");
    tab.value = "general";
  } catch (e) {
    if (e instanceof AppException && e.error.detail === "dsn") errors.value = { ...errors.value, dsn: e.error.message };
    else errors.value = { ...errors.value, dsn: t("form.errors.dsn") };
  }
}

function addParam() {
  form.params.push({ key: "", value: "" });
}

const err = (field: string) => errors.value[field];
</script>

<template>
  <BaseModal v-model:open="open" :title="isEdit ? t('form.titleEdit') : t('form.titleNew')" size="lg">
    <form class="flex flex-col gap-4" @submit.prevent="save(false)">
      <BaseTabs v-model="tab" :items="tabs">
        <template #general>
          <div class="grid grid-cols-2 gap-3 pt-4">
            <BaseInput v-model="form.name" :label="t('form.fields.name')" :error="err('name')" />
            <BaseSelect v-model="form.driver" :label="t('form.fields.driver')" :options="driverOptions" :error="err('driver')" />
            <BaseInput v-model="form.host" :label="t('form.fields.host')" :placeholder="t('form.hints.hostDefault')" :error="err('host')" mono />
            <BaseInput v-model="form.port" type="number" :label="t('form.fields.port')" :error="err('port')" mono />
            <BaseInput v-model="form.user" :label="t('form.fields.user')" :error="err('user')" autocomplete="off" />
            <PasswordField
              v-model="form.password"
              v-model:clear="form.clearPassword"
              :label="t('form.fields.password')"
              :saved="form.has.password && form.savePassword"
              :hint="form.savePassword ? undefined : t('form.hints.passwordNotSaved')"
              :error="err('password')"
            />
            <BaseCheckbox v-model="form.savePassword" class="col-span-2" :label="t('form.fields.savePassword')" />
            <BaseCheckbox v-model="form.noHistory" class="col-span-2" :label="t('form.fields.noHistory')" />
            <BaseInput v-model="form.database" class="col-span-2" :label="t('form.fields.database')" :error="err('database')" mono />
            <div class="col-span-2 flex flex-col gap-1">
              <span class="text-xs font-medium text-muted">{{ t("form.fields.color") }}</span>
              <div class="flex flex-wrap items-center gap-2" role="radiogroup" :aria-label="t('form.fields.color')">
                <button
                  type="button"
                  role="radio"
                  :aria-checked="!form.color"
                  class="size-6 rounded-full border border-border text-xs text-muted"
                  :class="!form.color && 'ring-2 ring-accent'"
                  @click="form.color = ''"
                >
                  ∅
                </button>
                <button
                  v-for="c in COLORS"
                  :key="c"
                  type="button"
                  role="radio"
                  :aria-checked="form.color === c"
                  :aria-label="c"
                  class="size-6 rounded-full"
                  :class="form.color === c && 'ring-2 ring-accent ring-offset-2 ring-offset-panel'"
                  :style="{ backgroundColor: c }"
                  @click="form.color = c"
                />
              </div>
              <p v-if="err('color')" class="text-xs text-danger">{{ err("color") }}</p>
            </div>
          </div>
        </template>

        <template #tls>
          <div class="grid grid-cols-1 gap-3 pt-4">
            <BaseSelect v-model="form.tls.mode" :label="t('form.fields.tlsMode')" :options="tlsOptions" :error="err('tls.mode')" />
            <PathField v-model="form.tls.caPath" :label="t('form.fields.caPath')" :error="err('tls.caPath')" />
            <PathField v-model="form.tls.certPath" :label="t('form.fields.certPath')" :error="err('tls.certPath')" />
            <PathField v-model="form.tls.keyPath" :label="t('form.fields.keyPath')" :error="err('tls.keyPath')" />
          </div>
        </template>

        <template #ssh>
          <div class="grid grid-cols-2 gap-3 pt-4">
            <BaseToggle v-model="form.ssh.enabled" class="col-span-2" :label="t('form.fields.sshEnabled')" />
            <template v-if="form.ssh.enabled">
              <BaseInput v-model="form.ssh.host" :label="t('form.fields.sshHost')" :error="err('ssh.host')" mono />
              <BaseInput v-model="form.ssh.port" type="number" :label="t('form.fields.sshPort')" :error="err('ssh.port')" mono />
              <BaseInput v-model="form.ssh.user" :label="t('form.fields.sshUser')" :error="err('ssh.user')" autocomplete="off" />
              <BaseSelect v-model="form.ssh.authMethod" :label="t('form.fields.sshMethod')" :options="sshMethodOptions" :error="err('ssh.authMethod')" />
              <PasswordField
                v-if="form.ssh.authMethod === 'password'"
                v-model="form.ssh.password"
                v-model:clear="form.ssh.clearPassword"
                class="col-span-2"
                :label="t('form.fields.sshPassword')"
                :saved="form.has.sshPassword && form.savePassword"
                :hint="form.savePassword ? undefined : t('form.hints.sshSecretsNotSaved')"
              />
              <template v-else>
                <PathField v-model="form.ssh.keyPath" class="col-span-2" :label="t('form.fields.sshKeyPath')" :error="err('ssh.keyPath')" />
                <PasswordField
                  v-model="form.ssh.passphrase"
                  v-model:clear="form.ssh.clearPassphrase"
                  class="col-span-2"
                  :label="t('form.fields.sshPassphrase')"
                  :saved="form.has.sshPassphrase && form.savePassword"
                  :hint="form.savePassword ? undefined : t('form.hints.sshSecretsNotSaved')"
                />
              </template>
            </template>
          </div>
        </template>

        <template #advanced>
          <div class="flex flex-col gap-2 pt-4">
            <p class="text-xs text-muted">{{ t("form.hints.advanced") }}</p>
            <datalist id="param-suggestions"><option v-for="s in PARAM_SUGGESTIONS" :key="s" :value="s" /></datalist>
            <div v-for="(row, i) in form.params" :key="i" class="grid grid-cols-[1fr_1fr_auto] items-start gap-2">
              <BaseInput v-model="row.key" :placeholder="t('form.advanced.key')" :error="err(`params.${i}`)" list="param-suggestions" mono />
              <BaseInput v-model="row.value" :placeholder="t('form.advanced.value')" mono />
              <BaseButton variant="ghost" :aria-label="t('form.buttons.removeParam')" @click="form.params.splice(i, 1)">×</BaseButton>
            </div>
            <BaseButton class="self-start" @click="addParam">{{ t("form.buttons.addParam") }}</BaseButton>
          </div>
        </template>

        <template #import>
          <div class="flex flex-col gap-3 pt-4">
            <BaseInput v-model="dsnText" :label="t('form.import.label')" :placeholder="t('form.import.placeholder')" :error="err('dsn')" mono />
            <p class="text-xs text-muted">{{ t("form.import.hint") }}</p>
            <div class="flex items-center gap-3">
              <BaseButton :disabled="!dsnText.trim()" @click="importDsn">{{ t("form.import.apply") }}</BaseButton>
              <span v-if="dsnInfo" class="text-xs text-success">{{ dsnInfo }}</span>
            </div>
          </div>
        </template>
      </BaseTabs>

      <TestResultPanel :running="!!testing" :result="testResult" @cancel="testing?.cancel()" @trust="trustAndRetest" />
      <button type="submit" class="hidden" tabindex="-1" aria-hidden="true" />
    </form>

    <template #footer>
      <BaseButton class="mr-auto" :disabled="!!testing || saving" @click="runTest">{{ t("form.buttons.test") }}</BaseButton>
      <BaseButton @click="uiConn.closeForm()">{{ t("form.buttons.cancel") }}</BaseButton>
      <BaseButton :loading="saving" @click="save(false)">{{ t("form.buttons.save") }}</BaseButton>
      <BaseButton variant="primary" :loading="saving" @click="save(true)">{{ t("form.buttons.saveConnect") }}</BaseButton>
    </template>
  </BaseModal>
</template>
