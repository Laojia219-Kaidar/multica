import { HIVE_CREW_WORKSPACE_FALLBACK_HOST } from "../product";

// Brand host shown when a deployment exposes no app URL. HiveCrew is
// local-first until a separately governed public domain is configured.
const BRAND_WORKSPACE_HOST = HIVE_CREW_WORKSPACE_FALLBACK_HOST;

/**
 * Host rendered as the `<host>/<slug>` workspace URL prefix in the
 * create-workspace and onboarding UI. Derived from the deployment's app URL
 * (`daemon_app_url` from `/api/config`, surfaced through the config store) so
 * self-hosted instances show their own domain instead of the fallback. Falls
 * back to the brand host when no app URL is configured.
 */
export function workspaceUrlHost(
  daemonAppUrl: string | null | undefined,
): string {
  const trimmed = daemonAppUrl?.trim();
  if (!trimmed) return BRAND_WORKSPACE_HOST;
  try {
    return new URL(trimmed).host || BRAND_WORKSPACE_HOST;
  } catch {
    // `daemon_app_url` may arrive without a scheme; treat it as a bare host
    // and strip any path/query/fragment so only the authority remains.
    const bare = trimmed
      .replace(/^.*?:\/\//, "")
      .replace(/[/?#].*$/, "")
      .trim();
    return bare || BRAND_WORKSPACE_HOST;
  }
}
