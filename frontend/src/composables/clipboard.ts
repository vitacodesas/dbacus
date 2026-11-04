import { Clipboard } from "@wailsio/runtime";

/**
 * Copia texto al portapapeles. `navigator.clipboard` puede rechazarse en la ventana nativa (sin gesto de usuario o sin
 * contexto seguro), así que se prueban en orden: la API del navegador, el portapapeles de Wails y `execCommand`.
 * Devuelve false solo si las tres fallan.
 */
export async function writeClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // sigue con la alternativa
  }
  try {
    await Clipboard.SetText(text);
    return true;
  } catch {
    // sigue con la última
  }
  return legacyCopy(text);
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;top:0;left:0;opacity:0;pointer-events:none";
  document.body.appendChild(ta);
  ta.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    ta.remove();
  }
}
