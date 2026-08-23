// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultStorage } from "./storage";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("defaultStorage", () => {
  it("round-trips get/set/remove through window.localStorage", () => {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => { values.delete(k); },
      setItem: (k, v) => { values.set(k, v); },
    };
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });

    expect(defaultStorage.getItem("k")).toBeNull();

    defaultStorage.setItem("k", "v");
    expect(defaultStorage.getItem("k")).toBe("v");
    expect(window.localStorage.getItem("k")).toBe("v");

    defaultStorage.removeItem("k");
    expect(defaultStorage.getItem("k")).toBeNull();
    expect(window.localStorage.getItem("k")).toBeNull();
  });

  // Node 25 ships a bare `localStorage` global even with no `window`; the
  // adapter must stay a no-op there instead of reading or writing it.
  it("never touches storage when window is absent", () => {
    const getItem = vi.fn(() => null);
    const setItem = vi.fn();
    const removeItem = vi.fn();
    vi.stubGlobal("window", undefined);
    vi.stubGlobal("localStorage", { getItem, setItem, removeItem });

    expect(defaultStorage.getItem("k")).toBeNull();
    defaultStorage.setItem("k", "v");
    defaultStorage.removeItem("k");

    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
    expect(removeItem).not.toHaveBeenCalled();
  });

  // Partial JSDOM / test surfaces: `window` exists but `window.localStorage`
  // is undefined. This is the failure that aborted useBatchDeleteIssues
  // before command-metric invalidation could be observed.
  it("is a safe no-op when window.localStorage is undefined", () => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: undefined,
    });

    expect(defaultStorage.getItem("k")).toBeNull();
    expect(() => defaultStorage.setItem("k", "v")).not.toThrow();
    expect(() => defaultStorage.removeItem("k")).not.toThrow();
  });

  it("is a safe no-op when window.localStorage is missing individual methods", () => {
    const setItem = vi.fn();
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      // Only setItem exists; getItem and removeItem are missing. This models
      // a partially implemented storage shim.
      value: { setItem },
    });

    expect(defaultStorage.getItem("k")).toBeNull();
    expect(() => defaultStorage.setItem("k", "v")).not.toThrow();
    expect(setItem).toHaveBeenCalledWith("k", "v");
    expect(() => defaultStorage.removeItem("k")).not.toThrow();
  });

  it("propagates real storage errors instead of hiding application exceptions", () => {
    const error = new Error("QuotaExceededError");
    const setItem = vi.fn(() => { throw error; });
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: { setItem },
    });

    expect(() => defaultStorage.setItem("k", "v")).toThrow(error);
  });
});
