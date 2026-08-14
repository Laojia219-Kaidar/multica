const LOOPBACK_HOSTS = new Set(["127.0.0.1", "localhost", "::1"]);

/**
 * Strict session cookies are site-bound.  If a local candidate frontend and
 * its API use different loopback host spellings, redirect only the frontend
 * host so the browser will send the cookie on the next request.  This never
 * changes a non-loopback URL and deliberately preserves the frontend port.
 */
export function canonicalLoopbackLoginUrl(currentHref: string, apiBaseUrl: string): string | null {
  try {
    const current = new URL(currentHref);
    const api = new URL(apiBaseUrl, current);
    if (
      current.protocol !== "http:" ||
      api.protocol !== "http:" ||
      !LOOPBACK_HOSTS.has(current.hostname) ||
      !LOOPBACK_HOSTS.has(api.hostname) ||
      current.hostname === api.hostname
    ) return null;
    current.hostname = api.hostname;
    return current.toString();
  } catch {
    return null;
  }
}
