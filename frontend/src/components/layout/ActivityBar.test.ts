import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";
import { useUiStore } from "../../stores/ui";
import ActivityBar from "./ActivityBar.vue";

beforeEach(() => setActivePinia(createPinia()));

describe("ActivityBar · Monitor", () => {
  it("el ítem Monitor alterna el área principal y no toca el panel lateral", async () => {
    const ui = useUiStore();
    const before = ui.sidebarView;
    const w = mount(ActivityBar, { global: { plugins: [i18n] } });
    const btn = w.get("[data-testid=open-monitor]");
    expect(btn.attributes("aria-pressed")).toBe("false");
    await btn.trigger("click");
    expect(ui.mainView).toBe("monitor");
    expect(btn.attributes("aria-pressed")).toBe("true");
    await btn.trigger("click");
    expect(ui.mainView).toBe("queries");
    expect(ui.sidebarView).toBe(before);
  });
});
