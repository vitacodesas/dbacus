import { ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/ping";
import { onPingTick } from "../api/events";

export const usePingStore = defineStore("ping", () => {
  const greeting = ref("");
  const lastTick = ref<number | null>(null);
  const pickedFile = ref("");
  let unsubscribe: (() => void) | null = null;

  async function greet(name: string) {
    greeting.value = await api.hello(name);
  }

  async function pick() {
    pickedFile.value = await api.pickFile();
  }

  function listen() {
    unsubscribe ??= onPingTick((tick) => (lastTick.value = tick.seq));
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = null;
  }

  return { greeting, lastTick, pickedFile, greet, pick, listen, stop };
});
