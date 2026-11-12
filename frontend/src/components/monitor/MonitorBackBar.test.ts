import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

vi.mock("../../api/queries", () => ({ saveTabs: vi.fn().mockResolvedValue(undefined), loadTabs: vi.fn().mockResolvedValue([]), closeTab: vi.fn() }));

import { i18n } from "../../i18n";
import MonitorBackBar from "./MonitorBackBar.vue";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useUiStore } from "../../stores/ui";

function mountBar() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const ui = useUiStore();
  ui.mainView = "monitor";
  const tabs = useQueryTabsStore();
  return { w: mount(MonitorBackBar, { global: { plugins: [pinia, i18n] } }), ui, tabs };
}

describe("MonitorBackBar", () => {
  beforeEach(() => vi.useRealTimers());

  it("sin pestañas solo ofrece volver", async () => {
    const { w, ui } = mountBar();
    expect(w.get("[data-testid=monitor-back-btn]").text()).toContain("Volver");
    expect(w.findAll("[role=listitem]")).toHaveLength(0);
    await w.get("[data-testid=monitor-back-btn]").trigger("click");
    expect(ui.mainView).toBe("queries");
  });

  it("lista las pestañas, marca las sin guardar y un chip activa la pestaña", async () => {
    const { w, tabs } = mountBar();
    const a = tabs.openTab({ profileId: "p1", sql: "SELECT 1" });
    const b = tabs.openTab({ profileId: "p1", sql: "SELECT 2" });
    tabs.setSql(b, "SELECT 22");
    await w.vm.$nextTick();
    expect(w.get("[data-testid=monitor-back-btn]").text()).toContain("(2)");
    expect(w.get(`[data-testid=monitor-back-tab-${b}]`).text()).toContain("●");
    expect(w.get(`[data-testid=monitor-back-tab-${a}]`).text()).not.toContain("●");
    await w.get(`[data-testid=monitor-back-tab-${a}]`).trigger("click");
    expect(tabs.activeTabId).toBe(a);
  });
});
