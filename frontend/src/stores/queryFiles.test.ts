import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const files = vi.hoisted(() => ({
  getRoot: vi.fn(), listQueries: vi.fn(), openQuery: vi.fn(), saveQuery: vi.fn(), createFolder: vi.fn(), moveQuery: vi.fn(), deleteQuery: vi.fn(),
  searchQueries: vi.fn(), pickRoot: vi.fn(), resetRoot: vi.fn(), reveal: vi.fn(),
}));
const queries = vi.hoisted(() => ({ saveTabs: vi.fn(), loadTabs: vi.fn(), closeTab: vi.fn() }));
vi.mock("../api/queryFiles", () => files);
vi.mock("../api/queries", () => queries);

import { AppException } from "../api/errors";
import { useConnectionsStore } from "./connections";
import { useQueryFilesStore } from "./queryFiles";
import { useQueryTabsStore } from "./queryTabs";

const appErr = (code: string, detail = "", message = "boom") => new AppException({ code, message, detail, retryable: false });
const tree = (nodes: unknown[] = []) => ({ root: { path: "/q", isDefault: true, exists: true, writable: true }, nodes, truncated: false });
const fileNode = (path: string, version: string) => ({ path, name: path.replace(/\.sql$/, ""), kind: "file", size: 1, modifiedAt: "2026-01-01T00:00:00Z", version, profileId: "", profileName: "", database: "" });

let tabs: ReturnType<typeof useQueryTabsStore>;
let store: ReturnType<typeof useQueryFilesStore>;

beforeEach(async () => {
  setActivePinia(createPinia());
  Object.values({ ...files, ...queries }).forEach((f) => f.mockReset());
  queries.saveTabs.mockResolvedValue(undefined);
  queries.closeTab.mockResolvedValue(undefined);
  queries.loadTabs.mockResolvedValue([]);
  files.listQueries.mockResolvedValue(tree());
  files.getRoot.mockResolvedValue(tree().root);
  vi.useFakeTimers();
  const connections = useConnectionsStore();
  connections.profiles = [{ id: "p1", name: "Prod", color: "" } as never];
  tabs = useQueryTabsStore();
  await tabs.restore();
  store = useQueryFilesStore();
});
afterEach(() => vi.useRealTimers());

describe("pestañas y «sin guardar»", () => {
  it("una pestaña abierta con SQL de partida (tabla, historial) no cuenta como sin guardar hasta editarla", () => {
    const id = tabs.openTab({ profileId: "p1", sql: "SELECT * FROM t" });
    expect(tabs.isDirty(tabs.byId(id)!)).toBe(false);
    expect(tabs.unsaved).toHaveLength(0);
    tabs.setSql(id, "SELECT 2");
    expect(tabs.isDirty(tabs.byId(id)!)).toBe(true);
    expect(tabs.unsaved.map((t) => t.id)).toEqual([id]);
  });

  it("cerrar una pestaña con cambios pregunta; «No guardar» la cierra y Cancelar la conserva", async () => {
    const id = tabs.openTab({ profileId: "p1" });
    tabs.setSql(id, "SELECT 1");
    await tabs.closeTab(id);
    expect(tabs.closePrompt).toBe(id);
    expect(tabs.byId(id)).toBeDefined();
    tabs.closePrompt = null; // Cancelar
    expect(tabs.byId(id)).toBeDefined();
    await tabs.closeTab(id, true);
    expect(tabs.byId(id)).toBeUndefined();
  });

  it("una pestaña vacía y sin archivo se cierra sin preguntar", async () => {
    const id = tabs.openTab({ profileId: "p1" });
    await tabs.closeTab(id);
    expect(tabs.closePrompt).toBeNull();
    expect(tabs.byId(id)).toBeUndefined();
  });

  it("persiste el vínculo con el archivo y lo restaura con el borrador", async () => {
    files.saveQuery.mockResolvedValue({ path: "a.sql", name: "a", version: "1:5" });
    const id = tabs.openTab({ profileId: "p1", sql: "v1" });
    tabs.bindFile(id, { path: "a.sql", title: "a", sql: "v1", version: "1:2", remember: true });
    tabs.setSql(id, "v1 editado");
    await tabs.persistNow();
    const saved = queries.saveTabs.mock.calls.at(-1)![0][0];
    expect(saved).toMatchObject({ filePath: "a.sql", baseSql: "v1", fileVersion: "1:2", sql: "v1 editado" });

    queries.loadTabs.mockResolvedValue([{ ...saved, sessionProfileId: "p1", order: 0 }]);
    const again = useQueryTabsStore();
    await again.restore();
    expect(again.tabs[0]).toMatchObject({ filePath: "a.sql", baseSql: "v1", sql: "v1 editado", fileVersion: "1:2" });
    expect(again.isDirty(again.tabs[0])).toBe(true);
  });
});

describe("guardar", () => {
  it("⌘S sobre una pestaña sin archivo abre el diálogo; el diálogo guarda y vincula", async () => {
    const id = tabs.openTab({ profileId: "p1" });
    tabs.setSql(id, "SELECT 1");
    expect(await store.save(id)).toBe(false);
    expect(store.saveDialog).toMatchObject({ tabId: id, remember: true });

    files.saveQuery.mockResolvedValue({ path: "Informes/ventas.sql", name: "ventas", version: "7:8" });
    store.saveDialog!.name = "ventas";
    store.saveDialog!.folder = "Informes";
    expect(await store.submitSave()).toBe(true);
    expect(files.saveQuery).toHaveBeenCalledWith(expect.objectContaining({ path: "Informes/ventas", sql: "SELECT 1", rememberConnection: true, expectedVersion: "", overwrite: false, profileId: "p1" }));
    expect(tabs.byId(id)).toMatchObject({ filePath: "Informes/ventas.sql", title: "ventas", baseSql: "SELECT 1", fileVersion: "7:8" });
    expect(tabs.isDirty(tabs.byId(id)!)).toBe(false);
    expect(store.saveDialog).toBeNull();
    expect(store.lastFolder).toBe("Informes");
  });

  it("un nombre existente deja el diálogo abierto con «Sobrescribir»", async () => {
    const id = tabs.openTab({ profileId: "p1" });
    tabs.setSql(id, "x");
    await store.save(id);
    files.saveQuery.mockRejectedValueOnce(appErr("VALIDATION", "exists", "Ya existe"));
    store.saveDialog!.name = "dup";
    expect(await store.submitSave()).toBe(false);
    expect(store.saveDialog).toMatchObject({ exists: true, busy: false });
    files.saveQuery.mockResolvedValueOnce({ path: "dup.sql", name: "dup", version: "1:1" });
    expect(await store.submitSave(true)).toBe(true);
    expect(files.saveQuery).toHaveBeenLastCalledWith(expect.objectContaining({ overwrite: true }));
  });

  it("guardar de nuevo envía la versión conocida; lo escrito durante el guardado sigue sin guardar", async () => {
    const id = tabs.openTab({ profileId: "p1", sql: "v1", file: { path: "a.sql", version: "1:2", remember: false } });
    tabs.setSql(id, "v2");
    files.saveQuery.mockImplementation(async () => {
      tabs.setSql(id, "v3"); // el usuario sigue escribiendo mientras Go guarda
      return { path: "a.sql", name: "a", version: "2:2" };
    });
    expect(await store.save(id)).toBe(true);
    expect(files.saveQuery).toHaveBeenCalledWith(expect.objectContaining({ path: "a.sql", expectedVersion: "1:2", sql: "v2", rememberConnection: false }));
    expect(tabs.byId(id)).toMatchObject({ baseSql: "v2", sql: "v3", fileVersion: "2:2" });
    expect(tabs.isDirty(tabs.byId(id)!)).toBe(true);
  });

  it("si el archivo cambió en el disco no pisa nada: marca la pestaña y ofrece recargar, mantener o sobrescribir", async () => {
    const id = tabs.openTab({ profileId: "p1", sql: "v1", file: { path: "a.sql", version: "1:2", remember: false } });
    tabs.setSql(id, "mío");
    files.saveQuery.mockRejectedValueOnce(appErr("VALIDATION", "changed_on_disk"));
    expect(await store.save(id)).toBe(false);
    expect(tabs.byId(id)!.diskState).toBe("changed");

    files.openQuery.mockResolvedValue({ path: "a.sql", name: "a", sql: "del disco", version: "9:9", profileId: "", profileName: "", database: "", profileMissing: false });
    await store.keepMine(id);
    expect(tabs.byId(id)).toMatchObject({ sql: "mío", baseSql: "del disco", fileVersion: "9:9", diskState: "ok" });
    expect(tabs.isDirty(tabs.byId(id)!)).toBe(true);

    tabs.setDiskState(id, "changed");
    await store.reload(id);
    expect(tabs.byId(id)).toMatchObject({ sql: "del disco", baseSql: "del disco", diskState: "ok" });

    files.saveQuery.mockResolvedValue({ path: "a.sql", name: "a", version: "10:1" });
    tabs.setSql(id, "otra");
    await store.overwrite(id);
    expect(files.saveQuery).toHaveBeenLastCalledWith(expect.objectContaining({ overwrite: true, expectedVersion: "" }));
  });

  it("«Guardar» en la pregunta de cierre guarda y cierra la pestaña", async () => {
    const id = tabs.openTab({ profileId: "p1", sql: "v1", file: { path: "a.sql", version: "1:2", remember: false } });
    tabs.setSql(id, "v2");
    files.saveQuery.mockResolvedValue({ path: "a.sql", name: "a", version: "2:2" });
    await store.save(id, { thenClose: true });
    expect(tabs.byId(id)).toBeUndefined();
  });

  it("guardar desde el historial no vincula ninguna pestaña", async () => {
    store.openSaveDialog({ tabId: null, sql: "SELECT 9", profileId: "p1", database: "app" });
    files.saveQuery.mockResolvedValue({ path: "h.sql", name: "h", version: "1:1" });
    store.saveDialog!.name = "h";
    expect(await store.submitSave()).toBe(true);
    expect(files.saveQuery).toHaveBeenCalledWith(expect.objectContaining({ sql: "SELECT 9", database: "app" }));
    expect(tabs.tabs).toHaveLength(0);
  });
});

describe("abrir", () => {
  const opened = { path: "a.sql", name: "a", sql: "SELECT 1", version: "1:8", profileId: "p1", profileName: "Prod", database: "shop", profileMissing: false };

  it("abre en una pestaña con la conexión y la base de la cabecera, y no la duplica si ya está abierta", async () => {
    files.openQuery.mockResolvedValue(opened);
    const id = await store.openFile("a.sql");
    expect(tabs.byId(id!)).toMatchObject({ profileId: "p1", database: "shop", sql: "SELECT 1", title: "a", filePath: "a.sql", remember: true });
    expect(await store.openFile("a.sql")).toBe(id);
    expect(files.openQuery).toHaveBeenCalledTimes(1);
    expect(tabs.tabs).toHaveLength(1);
  });

  it("con un perfil que ya no existe usa otra conexión y lo avisa", async () => {
    files.openQuery.mockResolvedValue({ ...opened, profileId: "borrado", profileMissing: true });
    const id = await store.openFile("a.sql");
    expect(tabs.byId(id!)!.profileId).toBe("p1");
  });

  it("un archivo que ya no existe avisa y recarga el listado", async () => {
    files.openQuery.mockRejectedValue(appErr("NOT_FOUND"));
    expect(await store.openFile("x.sql")).toBeNull();
    await vi.advanceTimersByTimeAsync(0);
    expect(files.listQueries).toHaveBeenCalled();
  });
});

describe("cambios en el disco", () => {
  it("sin cambios locales recarga sola; con cambios locales avisa; archivo borrado → missing", async () => {
    const a = tabs.openTab({ profileId: "p1", sql: "a", file: { path: "a.sql", version: "1:1", remember: false } });
    const b = tabs.openTab({ profileId: "p1", sql: "b", file: { path: "b.sql", version: "1:1", remember: false } });
    tabs.setSql(b, "b editado");
    const c = tabs.openTab({ profileId: "p1", sql: "c", file: { path: "c.sql", version: "1:1", remember: false } });
    files.listQueries.mockResolvedValue(tree([fileNode("a.sql", "2:1"), fileNode("b.sql", "2:1")]));
    files.openQuery.mockResolvedValue({ path: "a.sql", name: "a", sql: "a nuevo", version: "2:1", profileId: "", profileName: "", database: "", profileMissing: false });

    await store.syncTabs();
    expect(tabs.byId(a)).toMatchObject({ sql: "a nuevo", fileVersion: "2:1", diskState: "ok" });
    expect(tabs.byId(b)).toMatchObject({ sql: "b editado", diskState: "changed" });
    expect(tabs.byId(c)!.diskState).toBe("missing");
  });

  it("si la carpeta no se puede listar no afirma nada sobre las pestañas", async () => {
    const a = tabs.openTab({ profileId: "p1", sql: "a", file: { path: "a.sql", version: "1:1", remember: false } });
    files.listQueries.mockRejectedValue(appErr("VALIDATION", "root"));
    await store.syncTabs();
    expect(tabs.byId(a)!.diskState).toBe("ok");
    expect(store.error?.detail).toBe("root");
  });

  it("sin pestañas vinculadas no toca el disco al arrancar", async () => {
    tabs.openTab({ profileId: "p1", sql: "x" });
    await store.syncTabs();
    expect(files.listQueries).not.toHaveBeenCalled();
  });
});

describe("mover y borrar", () => {
  it("mover o renombrar actualiza las rutas de las pestañas afectadas", async () => {
    const id = tabs.openTab({ profileId: "p1", sql: "x", file: { path: "a/uno.sql", version: "1:1", remember: false } });
    files.moveQuery.mockResolvedValue([{ from: "a/uno.sql", to: "b/uno.sql" }]);
    await store.moveEntry("a", "b");
    expect(tabs.byId(id)).toMatchObject({ filePath: "b/uno.sql", title: "uno" });
  });

  it("borrar una carpeta deja sus pestañas con el texto y marcadas como inexistentes", async () => {
    const id = tabs.openTab({ profileId: "p1", sql: "x", file: { path: "a/uno.sql", version: "1:1", remember: false } });
    const other = tabs.openTab({ profileId: "p1", sql: "y", file: { path: "ab/dos.sql", version: "1:1", remember: false } });
    files.deleteQuery.mockResolvedValue(undefined);
    store.askDelete("a");
    await store.confirmDelete();
    expect(files.deleteQuery).toHaveBeenCalledWith("a");
    expect(tabs.byId(id)).toMatchObject({ diskState: "missing", sql: "x" });
    expect(tabs.byId(other)!.diskState).toBe("ok");
  });

  it("el diálogo de renombrar conserva el error de Go y no cierra", async () => {
    files.listQueries.mockResolvedValue(tree([fileNode("a.sql", "1:1")]));
    await store.load();
    store.openEntryDialog("rename", "a.sql");
    expect(store.entryDialog).toMatchObject({ name: "a", kind: "rename" });
    files.moveQuery.mockRejectedValueOnce(appErr("VALIDATION", "exists", "Ya existe algo con ese nombre"));
    store.entryDialog!.name = "b";
    expect(await store.submitEntry()).toBe(false);
    expect(store.entryDialog).toMatchObject({ error: "Ya existe algo con ese nombre", busy: false });
    expect(files.moveQuery).toHaveBeenCalledWith("a.sql", "b");
  });
});

describe("búsqueda", () => {
  it("descarta la respuesta de una búsqueda anterior", async () => {
    let first!: (v: unknown) => void;
    files.searchQueries.mockImplementationOnce(() => new Promise((r) => (first = r)));
    files.searchQueries.mockResolvedValueOnce([{ path: "b.sql", name: "b", line: 0, snippet: "" }]);
    const p1 = store.search("uno", false);
    await store.search("dos", true);
    first([{ path: "a.sql", name: "a", line: 0, snippet: "" }]);
    await p1;
    expect(store.hits?.map((h) => h.path)).toEqual(["b.sql"]);
    expect(files.searchQueries).toHaveBeenLastCalledWith({ text: "dos", content: true, limit: 50 });
    await store.search("  ", false);
    expect(store.hits).toBeNull();
  });
});
