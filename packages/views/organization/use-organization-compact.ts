"use client";

import { useEffect, useState } from "react";

const COMPACT_BREAKPOINT = 720;

/**
 * True when the viewport is at or below the organization center's single-column
 * breakpoint (720px). Mirrors the outcome center contract so the two
 * master-detail surfaces behave identically on narrow screens.
 */
export function useOrganizationCompact(): boolean {
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