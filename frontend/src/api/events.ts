import { Events } from "@wailsio/runtime";
import type { ExportDone, ExportProgress, HistoryAdded, QueryDone, QueryError, QueryProgress, QueryStarted, QueryStatement, SchemaInvalidated, SessionClosed } from "../types";

export interface PingTick {
  seq: number;
  at: string;
}

type Unsubscribe = () => void;

export function onPingTick(handler: (tick: PingTick) => void): Unsubscribe {
  return Events.On("ping:tick", (event: { data: PingTick }) => handler(event.data));
}

export function onSessionClosed(handler: (ev: SessionClosed) => void): Unsubscribe {
  return Events.On("session:closed", (event: { data: SessionClosed }) => handler(event.data));
}

export function onSchemaInvalidated(handler: (ev: SchemaInvalidated) => void): Unsubscribe {
  return Events.On("schema:invalidated", (event: { data: SchemaInvalidated }) => handler(event.data));
}

export function onQueryStarted(h: (ev: QueryStarted) => void): Unsubscribe {
  return Events.On("query:started", (e: { data: QueryStarted }) => h(e.data));
}
export function onQueryStatement(h: (ev: QueryStatement) => void): Unsubscribe {
  return Events.On("query:statement", (e: { data: QueryStatement }) => h(e.data));
}
export function onQueryProgress(h: (ev: QueryProgress) => void): Unsubscribe {
  return Events.On("query:progress", (e: { data: QueryProgress }) => h(e.data));
}
export function onQueryDone(h: (ev: QueryDone) => void): Unsubscribe {
  return Events.On("query:done", (e: { data: QueryDone }) => h(e.data));
}
export function onQueryError(h: (ev: QueryError) => void): Unsubscribe {
  return Events.On("query:error", (e: { data: QueryError }) => h(e.data));
}

export function onHistoryAdded(h: (ev: HistoryAdded) => void): Unsubscribe {
  return Events.On("history:added", (e: { data: HistoryAdded }) => h(e.data));
}
export function onExportProgress(h: (ev: ExportProgress) => void): Unsubscribe {
  return Events.On("export:progress", (e: { data: ExportProgress }) => h(e.data));
}
export function onExportDone(h: (ev: ExportDone) => void): Unsubscribe {
  return Events.On("export:done", (e: { data: ExportDone }) => h(e.data));
}
