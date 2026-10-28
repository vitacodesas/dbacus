import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/connections";
import { asAppError } from "../api/errors";
import type { AppError, ConnectionProfile, ConnectionSecrets, DriverInfo } from "../types";

const byName = (a: ConnectionProfile, b: ConnectionProfile) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" });

export const useConnectionsStore = defineStore("connections", () => {
  const profiles = ref<ConnectionProfile[]>([]);
  const drivers = ref<DriverInfo[]>([]);
  const loading = ref(false);
  const loaded = ref(false);
  const lastError = ref<AppError | null>(null);

  const byId = computed(() => new Map(profiles.value.map((p) => [p.id, p])));

  function upsert(p: ConnectionProfile) {
    const i = profiles.value.findIndex((x) => x.id === p.id);
    if (i >= 0) profiles.value[i] = p;
    else profiles.value.push(p);
    profiles.value.sort(byName);
  }

  async function load() {
    loading.value = true;
    lastError.value = null;
    try {
      [profiles.value, drivers.value] = await Promise.all([api.listProfiles(), api.listDrivers()]);
      profiles.value.sort(byName);
      loaded.value = true;
    } catch (e) {
      lastError.value = asAppError(e);
    } finally {
      loading.value = false;
    }
  }

  async function save(profile: ConnectionProfile, secrets: ConnectionSecrets) {
    const saved = await api.saveProfile(profile, secrets);
    upsert(saved);
    return saved;
  }

  async function clone(id: string) {
    const copy = await api.cloneProfile(id);
    upsert(copy);
    return copy;
  }

  async function remove(id: string, deleteHistory: boolean) {
    await api.deleteProfile(id, deleteHistory);
    profiles.value = profiles.value.filter((p) => p.id !== id);
  }

  return { profiles, drivers, loading, loaded, lastError, byId, load, save, clone, remove };
});
