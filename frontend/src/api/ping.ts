// Único punto que importa bindings generados (spec 01/02); el resto del front usa estas funciones.
import { PingService } from "../../bindings/tabulariswails/internal/services";

export const hello = (name: string): Promise<string> => PingService.Hello(name);
export const pickFile = (): Promise<string> => PingService.PickFile();
