import type { StorageAdapter } from "../types/storage";

/**
 * SSR-safe localStorage. Works in both Next.js (SSR) and Electron (always client).
 *
 * Storage is reached through `window.localStorage`, never the bare global:
 * Node 25 ships its own partial `localStorage` that exists without a `window`,
 * and vitest/jsdom exposes one that is missing methods — reading the property
 * off the guarded `window` is the only access that behaves in browsers, JSDOM
 * and window-less runtimes alike.
 */
export const defaultStorage: StorageAdapter = {
  getItem: (k) =>
    typeof window !== "undefined" ? window.localStorage.getItem(k) : null,
  setItem: (k, v) => {
    if (typeof window !== "undefined") window.localStorage.setItem(k, v);
  },
  removeItem: (k) => {
    if (typeof window !== "undefined") window.localStorage.removeItem(k);
  },
};
