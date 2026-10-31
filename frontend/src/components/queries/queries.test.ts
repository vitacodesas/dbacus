import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const files = vi.hoisted(() => ({
  getRoot: vi.fn(), listQueries: vi.fn(), openQuery: vi.fn(), saveQuery: vi.fn(), createFolder: vi.fn(), moveQuery: vi.fn(), deleteQuery: vi.fn(),
  searchQueries: vi.fn(), pickRoot: vi.fn(), resetRoot: vi.fn(), reveal: vi.fn(),
}));
const queries = vi.hoisted(() => ({ saveTabs: vi.fn(), loadTabs: vi.fn(), closeTab: vi.fn() }));
vi.mock("../../api/queryFiles", () => files);
vi.mock("../../api/queries", () => queries);

import { useConnectionsStore } from "../../stores/connections";
import { useQueryFilesStore } from "../../stores/queryFiles";
import { useQueryTabsStore } from "../../stores/queryTabs";
import QueriesPanel from "./QueriesPanel.vue";
import SaveQueryDialog from "./SaveQueryDialog.vue";
import FileBanner from "./FileBanner.vue";

const node = (path: string, kind: "file" | "folder", extra: Record<string, unknown> = {}) =>
  ({ path, name: path.slice(path.lastIndexOf("/") + 1).replace(/\.sql$/, ""), kind, size: 1, modifiedAt: "2026-01-01T00:00:00Z", version: "1:1", profileId: "", profileName: "", database: "", ...extra });
const root = { path: "/Docs/Dbacus/Consultas", isDefault: true, exists: true, writable: true };

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values({ ...files, ...queries }).forEach((f) => f.mockReset());
  queries.saveTabs.mockResolvedValue(undefined);
  queries.loadTabs.mockResolvedValue([]);
  files.listQueries.mockResolvedValue({ root, truncated: false, nodes: [node("Informes", "folder"), node("Informes/ventas.sql", "file", { profileId: "p1", database: "shop" }), node("a.sql", "file")] });
  files.getRoot.mockResolvedValue(root);
  useConnectionsStore().profiles = [{ id: "p1", name: "Prod", color: "#ff0000" } as never];
});

describe("QueriesPanel", () => {
  it("lista carpetas y archivos, muestra la ruta de la carpeta y abre el archivo con doble clic", async () => {
    files.openQuery.mockResolvedValue({ path: "a.sql", name: "a", sql: "SELECT 1", version: "1:1", profileId: "", profileName: "", database: "", profileMissing: false });
    const w = mount(QueriesPanel, { global: { plugins: [i18n] } });
    await flushPromises();
    const rows = w.findAll('[role="treeitem"]');
    expect(rows.map((r) => r.attributes("data-path"))).toEqual(["Informes", "Informes/ventas.sql", "a.sql"]);
    expect(w.get('[data-testid="queries-root"]').text()).toContain("Consultas");
    await rows[2].trigger("dblclick");
    await flushPromises();
    expect(files.openQuery).toHaveBeenCalledWith("a.sql");
    expect(useQueryTabsStore().tabs[0]).toMatchObject({ filePath: "a.sql", sql: "SELECT 1" });
  });

  it("un clic en la ruta la muestra en el explorador", async () => {
    files.reveal.mockResolvedValue(undefined);
    const w = mount(QueriesPanel, { global: { plugins: [i18n] } });
    await flushPromises();
    await w.get('[data-testid="queries-root"]').trigger("click");
    expect(files.reveal).toHaveBeenCalledWith("");
  });

  it("pliega una carpeta con un clic", async () => {
    const w = mount(QueriesPanel, { global: { plugins: [i18n] } });
    await flushPromises();
    await w.get('[data-path="Informes"]').trigger("click");
    expect(w.findAll('[role="treeitem"]').map((r) => r.attributes("data-path"))).toEqual(["Informes", "a.sql"]);
  });

  it("muestra lo que está abierto y sin guardar, y busca por contenido con debounce", async () => {
    vi.useFakeTimers();
    const tabs = useQueryTabsStore();
    await tabs.restore();
    const id = tabs.openTab({ profileId: "p1" });
    tabs.setSql(id, "SELECT 1");
    files.searchQueries.mockResolvedValue([{ path: "a.sql", name: "a", line: 3, snippet: "SELECT total" }]);
    const w = mount(QueriesPanel, { global: { plugins: [i18n] } });
    await flushPromises();
    expect(w.get('[data-testid="queries-unsaved"]').text()).toContain("Consulta 1");

    await w.get('[data-testid="queries-search"]').setValue("total");
    await w.get('[data-testid="queries-search-content"] input').setValue(true);
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    expect(files.searchQueries).toHaveBeenCalledTimes(1);
    expect(files.searchQueries).toHaveBeenCalledWith({ text: "total", content: true, limit: 50 });
    expect(w.get('[data-testid="queries-hits"]').text()).toContain("3: SELECT total");
    expect(w.find('[data-testid="queries-unsaved"]').exists()).toBe(false);
    vi.useRealTimers();
  });

  it("si la carpeta no se puede abrir lo explica y ofrece elegir otra", async () => {
    files.listQueries.mockRejectedValue(Object.assign(new Error("x"), { name: "AppException", error: { code: "VALIDATION", message: "x", detail: "root", retryable: false } }));
    files.getRoot.mockResolvedValue({ path: "/Docs/x", isDefault: true, exists: false, writable: false });
    const w = mount(QueriesPanel, { global: { plugins: [i18n] } });
    await flushPromises();
    expect(w.get('[data-testid="queries-root-error"]').text()).toContain("/Docs/x");
  });
});

describe("SaveQueryDialog", () => {
  it("muestra la ruta final completa y avisa cuando el nombre existe", async () => {
    const q = useQueryFilesStore();
    await q.load();
    q.openSaveDialog({ tabId: null, sql: "SELECT 1", profileId: "p1", database: "shop", name: "ventas" });
    q.saveDialog!.folder = "Informes";
    const w = mount(SaveQueryDialog, { global: { plugins: [i18n] }, attachTo: document.body });
    await flushPromises();
    expect(document.body.querySelector('[data-testid="save-query-path"]')?.textContent).toBe("/Docs/Dbacus/Consultas/Informes/ventas.sql");
    q.saveDialog!.exists = true;
    q.saveDialog!.error = "Ya existe";
    await flushPromises();
    expect(document.body.querySelector('[data-testid="save-query-overwrite"]')).not.toBeNull();
    w.unmount();
  });
});

describe("FileBanner", () => {
  const tab = (diskState: string) => ({ id: "t", filePath: "a.sql", diskState }) as never;
  it("ofrece recargar, mantener o sobrescribir si cambió, y guardar de nuevo si desapareció", () => {
    const changed = mount(FileBanner, { props: { tab: tab("changed") }, global: { plugins: [i18n] } });
    expect(["banner-reload", "banner-keep", "banner-overwrite"].every((id) => changed.find(`[data-testid="${id}"]`).exists())).toBe(true);
    const missing = mount(FileBanner, { props: { tab: tab("missing") }, global: { plugins: [i18n] } });
    expect(missing.find('[data-testid="banner-save-again"]').exists()).toBe(true);
    expect(missing.find('[data-testid="banner-reload"]').exists()).toBe(false);
    expect(mount(FileBanner, { props: { tab: tab("ok") }, global: { plugins: [i18n] } }).find('[data-testid="file-banner"]').exists()).toBe(false);
  });
});
