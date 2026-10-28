import { describe, expect, it } from "vitest";
import { AppException, ErrorCode, toAppError, wrap } from "./errors";

describe("toAppError", () => {
  it("extrae el AppError de `cause` (objeto)", () => {
    const e = { name: "RuntimeError", message: "x", cause: { code: "AUTH_FAILED", message: "mal", detail: "d", retryable: false } };
    expect(toAppError(e)).toEqual({ code: "AUTH_FAILED", message: "mal", detail: "d", retryable: false });
  });
  it("acepta `cause` como JSON en string y rellena retryable", () => {
    const e = { cause: JSON.stringify({ code: "TIMEOUT", message: "tarda" }) };
    expect(toAppError(e)).toEqual({ code: "TIMEOUT", message: "tarda", retryable: false });
  });
  it("traduce la cancelación", () => {
    expect(toAppError({ name: "CancelError" }).code).toBe(ErrorCode.QueryCanceled);
  });
  it("lo desconocido es INTERNAL con el mensaje original", () => {
    expect(toAppError(new Error("boom"))).toMatchObject({ code: "INTERNAL", message: "boom" });
    expect(toAppError(null).code).toBe("INTERNAL");
    expect(toAppError({ cause: { foo: 1 } }).code).toBe("INTERNAL");
  });
  it("conserva el error de una AppException", () => {
    const err = { code: "NOT_FOUND", message: "no", retryable: false };
    expect(toAppError(new AppException(err))).toBe(err);
  });
});

describe("wrap", () => {
  it("rechaza siempre con AppException", async () => {
    await expect(wrap(Promise.reject({ cause: { code: "VALIDATION", message: "m", detail: "name" } }))).rejects.toMatchObject({
      name: "AppException",
      error: { code: "VALIDATION", detail: "name" },
    });
    await expect(wrap(Promise.resolve(3))).resolves.toBe(3);
  });
});
