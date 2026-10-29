// Único punto que importa SettingsService (bindings generados). Ver docs/specs/09-estado-pinia.md.
import { SettingsService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { Settings } from "../types";
import { wrap } from "./errors";

export const getSettings = (): Promise<Settings> => wrap(SettingsService.Get());
export const setSettings = (s: Settings): Promise<Settings> => wrap(SettingsService.Set(s));
