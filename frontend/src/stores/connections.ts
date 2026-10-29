import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/connections";
import { asAppError } from "../api/errors";
import { useUiStore } from "./ui";
import type { AppError, ConnectionGroup, ConnectionProfile, ConnectionSecrets, DriverInfo } from "../types";

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" });

export const useConnectionsStore = defineStore("connections", () => {
  const profiles = ref<ConnectionProfile[]>([]);
  const groups = ref<ConnectionGroup[]>([]);
  const drivers = ref<DriverInfo[]>([]);
  const loading = ref(false);
  const loaded = ref(false);
  const lastError = ref<AppError | null>(null);

  const byId = computed(() => new Map(profiles.value.map((p) => [p.id, p])));
  const groupById = computed(() => new Map(groups.value.map((g) => [g.id, g])));

  /** Grupo de una conexión; un groupId huérfano (grupo inexistente) cuenta como «Sin grupo» (spec 17). */
  const groupOf = (p?: ConnectionProfile) => (p?.groupId ? groupById.value.get(p.groupId) : undefined);

  /** Color efectivo (spec 17): el de la conexión si lo tiene; si no, el de su grupo; si no, ninguno. */
  const colorOf = (p?: ConnectionProfile) => p?.color || groupOf(p)?.color || "";

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
      [profiles.value, drivers.value, groups.value] = await Promise.all([api.listProfiles(), api.listDrivers(), api.listGroups()]);
      profiles.value.sort(byName);
      groups.value.sort(byName);
      useUiStore().pruneGroups(groups.value.map((g) => g.id));
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

  async function saveGroup(group: ConnectionGroup) {
    const saved = await api.saveGroup(group);
    const i = groups.value.findIndex((g) => g.id === saved.id);
    if (i >= 0) groups.value[i] = saved;
    else groups.value.push(saved);
    groups.value.sort(byName);
    return saved;
  }

  /** Las conexiones del grupo pasan a «Sin grupo» en Go: se vuelven a pedir en vez de reproducirlo aquí. */
  async function removeGroup(id: string) {
    await api.deleteGroup(id);
    groups.value = groups.value.filter((g) => g.id !== id);
    profiles.value = (await api.listProfiles()).sort(byName);
  }

  return { profiles, groups, drivers, loading, loaded, lastError, byId, groupById, groupOf, colorOf, load, save, clone, remove, saveGroup, removeGroup };
});
