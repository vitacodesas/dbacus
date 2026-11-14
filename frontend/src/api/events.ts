import { Events } from "@wailsio/runtime";
import type { EditApplied, ExportDone, ExportProgress, HistoryAdded, QueryDone, QueryError, QueryProgress, QueryStarted, QueryStatement, SchemaInvalidated, SessionClosed, MonitorProcessesEvent, MonitorSampleEvent, MonitorStateEvent, MigrateProgressEvent, MigrateStateEvent, MigrateDoneEvent } from "../types";

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
export function onEditApplied(h: (ev: EditApplied) => void): Unsubscribe {
  return Events.On("edit:applied", (e: { data: EditApplied }) => h(e.data));
}
export function onExportProgress(h: (ev: ExportProgress) => void): Unsubscribe {
  return Events.On("export:progress", (e: { data: ExportProgress }) => h(e.data));
}
export function onExportDone(h: (ev: ExportDone) => void): Unsubscribe {
  return Events.On("export:done", (e: { data: ExportDone }) => h(e.data));
}

export function onMonitorSample(h: (ev: MonitorSampleEvent) => void): Unsubscribe {
  return Events.On("monitor:sample", (e: { data: MonitorSampleEvent }) => h(e.data));
}
export function onMonitorState(h: (ev: MonitorStateEvent) => void): Unsubscribe {
  return Events.On("monitor:state", (e: { data: MonitorStateEvent }) => h(e.data));
}
export function onMonitorProcesses(h: (ev: MonitorProcessesEvent) => void): Unsubscribe {
  return Events.On("monitor:processes", (e: { data: MonitorProcessesEvent }) => h(e.data));
}

export function onMigrateProgress(h: (ev: MigrateProgressEvent) => void): Unsubscribe {
  return Events.On("migrate:progress", (e: { data: MigrateProgressEvent }) => h(e.data));
}
export function onMigrateState(h: (ev: MigrateStateEvent) => void): Unsubscribe {
  return Events.On("migrate:state", (e: { data: MigrateStateEvent }) => h(e.data));
}
export function onMigrateDone(h: (ev: MigrateDoneEvent) => void): Unsubscribe {
  return Events.On("migrate:done", (e: { data: MigrateDoneEvent }) => h(e.data));
}
