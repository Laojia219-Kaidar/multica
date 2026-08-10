export function isOfficialMarketingHost(
  hostname: string,
  publicAppUrl: string | undefined = process.env.HIVECREW_PUBLIC_APP_URL,
): boolean {
  if (!publicAppUrl) return false;

  let officialHostname: string;
  try {
    officialHostname = new URL(publicAppUrl).hostname;
  } catch {
    return false;
  }
  const normalized = hostname.trim().toLowerCase().replace(/\.$/, "");
  return normalized === officialHostname.toLowerCase();
}
