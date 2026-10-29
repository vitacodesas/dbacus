import { afterAll } from "vitest";

// @wailsio/runtime lanza al importarse un setInterval de 50 ms (drag.js) cuyo callback usa `window`. En jsdom `window`
// es globalThis, así que ese temporizador es el real de Node y sobrevive al desmontaje del entorno: si dispara en el
// hueco entre ese desmontaje y el fin del proceso (más ancho en runners lentos), vitest informa «window is not
// defined» y sale con error aunque todos los tests pasen. Se limpian al acabar cada archivo, con `window` aún vivo.
const intervals = new Set<ReturnType<typeof setInterval>>();
const realSetInterval = globalThis.setInterval;
globalThis.setInterval = ((...args: Parameters<typeof setInterval>) => {
  const id = realSetInterval(...args);
  intervals.add(id);
  return id;
}) as typeof setInterval;

afterAll(() => {
  intervals.forEach((id) => clearInterval(id));
  intervals.clear();
});
