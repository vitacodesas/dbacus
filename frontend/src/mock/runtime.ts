// Sustituto de @wailsio/runtime en modo VITE_MOCK_API (E2E con Playwright, spec 11): solo el bus de eventos.
type Handler = (event: { name: string; data: unknown }) => void;
const handlers = new Map<string, Set<Handler>>();

export const Events = {
  On(name: string, cb: Handler): () => void {
    const set = handlers.get(name) ?? new Set<Handler>();
    set.add(cb);
    handlers.set(name, set);
    return () => void set.delete(cb);
  },
  Emit(name: string, data?: unknown) {
    // Asíncrono, como los eventos reales: nunca se entregan dentro de la llamada que los provocó.
    queueMicrotask(() => handlers.get(name)?.forEach((h) => h({ name, data })));
  },
};
