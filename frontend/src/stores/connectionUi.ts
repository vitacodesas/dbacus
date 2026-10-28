import { ref } from "vue";
import { defineStore } from "pinia";

/** Qué diálogos de gestión de conexiones están abiertos. */
export const useConnectionUiStore = defineStore("connectionUi", () => {
  const formOpen = ref(false);
  /** null = nueva conexión. */
  const editingId = ref<string | null>(null);
  const deleteTargetId = ref<string | null>(null);

  function openNew() {
    editingId.value = null;
    formOpen.value = true;
  }
  function openEdit(id: string) {
    editingId.value = id;
    formOpen.value = true;
  }
  function closeForm() {
    formOpen.value = false;
  }

  return { formOpen, editingId, deleteTargetId, openNew, openEdit, closeForm };
});
