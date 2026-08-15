// @vitest-environment jsdom
import { beforeAll, describe, expect, it, vi } from "vitest";
import { defaultStorage } from "./storage";

// Node 25 ships a partial `localStorage` shim under jsdom that's missing
// `clear`/`removeItem`; replace it with a real in-memory Storage so the
// round-trip can hold values.
beforeAll(() => {
  if (typeof globalThis.localStorage?.clear !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => { values.delete(k); },
      setItem: (k, v) => { values.set(k, v); },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  }
});

describe("defaultStorage", () => {
  it("round-trips get/set/remove through window.localStorage", () => {
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
    try {
      expect(defaultStorage.getItem("k")).toBeNull();
      defaultStorage.setItem("k", "v");
      defaultStorage.removeItem("k");
    } finally {
      vi.unstubAllGlobals();
    }
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
    expect(removeItem).not.toHaveBeenCalled();
  });
});
