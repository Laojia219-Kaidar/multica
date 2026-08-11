"use client";

import { useEffect, useState } from "react";

const COMPACT_BREAKPOINT = 720;

/**
 * True when the viewport is at or below the outcome center's single-column
 * breakpoint (720px). Deliberately independent of the shared 768px
 * `useIsMobile` — the outcome list/detail contract pins the toggle at 720px.
 */
export function useOutcomesCompact(): boolean {
  const [compact, setCompact] = useState<boolean | undefined>(undefined);

  useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${COMPACT_BREAKPOINT}px)`);
    const onChange = () => setCompact(window.innerWidth <= COMPACT_BREAKPOINT);
    mql.addEventListener("change", onChange);
    setCompact(window.innerWidth <= COMPACT_BREAKPOINT);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return !!compact;
}