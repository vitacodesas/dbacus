import type { AppError } from "../types";

// Códigos de AppError (mantener alineado con internal/apperr y docs/specs/02).
export const ErrorCode = {
  Validation: "VALIDATION",
  NotFound: "NOT_FOUND",
  AuthFailed: "AUTH_FAILED",
  NetworkUnreachable: "NETWORK_UNREACHABLE",
  Timeout: "TIMEOUT",
  TLSError: "TLS_ERROR",
  SSHAuthFailed: "SSH_AUTH_FAILED",
  SSHHostKeyMismatch: "SSH_HOSTKEY_MISMATCH",
  SSHHostKeyUnknown: "SSH_HOSTKEY_UNKNOWN",
  KeyringUnavailable: "KEYRING_UNAVAILABLE",
  PasswordRequired: "PASSWORD_REQUIRED",
  SessionClosed: "SESSION_CLOSED",
  QuerySyntax: "QUERY_SYNTAX",
  QueryCanceled: "QUERY_CANCELED",
  QueryFailed: "QUERY_FAILED",
  ResultExpired: "RESULT_EXPIRED",
  Unsupported: "UNSUPPORTED",
  Internal: "INTERNAL",
} as const;

/** Excepción que lanza `api/`; los stores capturan esta clase y no errores crudos del puente. */
export class AppException extends Error {
  readonly error: AppError;
  constructor(error: AppError) {
    super(error.message);
    this.name = "AppException";
    this.error = error;
  }
}

const isAppError = (v: unknown): v is AppError =>
  typeof v === "object" && v !== null && typeof (v as AppError).code === "string" && typeof (v as AppError).message === "string";

/** Convierte lo que rechaza el puente de Wails (RuntimeError con `cause`) en un AppError. */
export function toAppError(e: unknown): AppError {
  if (e instanceof AppException) return e.error;
  const err = e as { name?: string; message?: string; cause?: unknown } | null;
  let cause = err?.cause;
  if (typeof cause === "string") {
    try {
      cause = JSON.parse(cause);
    } catch {
      /* no era JSON */
    }
  }
  if (isAppError(cause)) return { ...cause, retryable: cause.retryable === true };
  if (err?.name === "CancelError" || err?.name === "CancelledError") {
    return { code: ErrorCode.QueryCanceled, message: "Operación cancelada", retryable: false };
  }
  return { code: ErrorCode.Internal, message: err?.message || "Error inesperado", retryable: false };
}

/** Envuelve una promesa del puente para que rechace siempre con AppException. */
export async function wrap<T>(p: PromiseLike<T>): Promise<T> {
  try {
    return await p;
  } catch (e) {
    throw new AppException(toAppError(e));
  }
}

export function asAppError(e: unknown): AppError {
  return toAppError(e);
}
