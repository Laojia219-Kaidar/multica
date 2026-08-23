import type { StorageAdapter } from "../types/storage";

/**
 * SSR-safe localStorage. Works in browsers, Next.js SSR, Electron, Node and
 * partial JSDOM surfaces.
 *
 * Storage is reached only through a guarded `window.localStorage`. Node 25
 * ships its own partial `localStorage` global without a `window`, and some
 * jsdom/test environments expose `window` but leave `window.localStorage`
 * undefined or missing methods. We therefore:
 *
 * - never read the bare global `localStorage`;
 * - no-op when `window` or `window.localStorage` is absent;
 * - no-op when the specific method we need is missing (partial interface);
 * - let genuine `setItem`/`getItem`/`removeItem` errors propagate so quota
 *   and security exceptions remain visible to the caller instead of being
 *   silently swallowed.
 */
function getBrowserStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  const candidate = window.localStorage;
  if (!candidate) return null;
  return candidate;
}

export const defaultStorage: StorageAdapter = {
  getItem: (k) => {
    const storage = getBrowserStorage();
    if (!storage || typeof storage.getItem !== "function") return null;
    return storage.getItem(k);
  },
  setItem: (k, v) => {
    const storage = getBrowserStorage();
    if (!storage || typeof storage.setItem !== "function") return;
    storage.setItem(k, v);
  },
  removeItem: (k) => {
    const storage = getBrowserStorage();
    if (!storage || typeof storage.removeItem !== "function") return;
    storage.removeItem(k);
  },
};
