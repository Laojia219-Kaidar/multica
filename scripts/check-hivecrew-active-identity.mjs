import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");

async function text(path) {
  return readFile(resolve(root, path), "utf8");
}

function requireMatch(source, pattern, label, failures) {
  if (!pattern.test(source)) failures.push(`missing ${label}`);
}

function forbidMatch(source, pattern, label, failures) {
  if (pattern.test(source)) failures.push(`forbidden ${label}`);
}

const failures = [];
const rootPackage = JSON.parse(await text("package.json"));
const desktopPackage = JSON.parse(await text("apps/desktop/package.json"));
const builder = await text("apps/desktop/electron-builder.yml");
const runtime = await text("apps/desktop/src/shared/runtime-config.ts");
const loader = await text("apps/desktop/src/main/runtime-config-loader.ts");
const bootstrap = await text("apps/desktop/src/main/cli-bootstrap.ts");
const desktopMain = await text("apps/desktop/src/main/index.ts");
const webLayout = await text("apps/web/app/layout.tsx");
const webLogin = await text("apps/web/app/(auth)/login/page.tsx");
const webAuthCallback = await text("apps/web/app/auth/callback/page.tsx");
const webPublicHost = await text("apps/web/lib/public-host.ts");
const webExternalLinks = await text(
  "apps/web/features/landing/components/shared.tsx",
);
const webReleaseSource = await text(
  "apps/web/features/landing/utils/github-release.ts",
);
const remoteRuntimeDialog = await text(
  "packages/views/runtimes/components/connect-remote-dialog.tsx",
);
const helpLauncher = await text("packages/views/layout/help-launcher.tsx");
const feedbackModal = await text("packages/views/modals/feedback.tsx");
const desktopDaemonSettings = await text(
  "apps/desktop/src/renderer/src/components/daemon-settings-tab.tsx",
);
const desktopRecovery = await text("apps/desktop/src/main/renderer-recovery.ts");
const desktopRouteError = await text(
  "apps/desktop/src/renderer/src/components/route-error-page.tsx",
);
const desktopRendererHtml = await text("apps/desktop/src/renderer/index.html");
const landingMetadata = (
  await Promise.all([
    text("apps/web/app/(landing)/download/page.tsx"),
    text("apps/web/app/(landing)/about/page.tsx"),
    text("apps/web/app/(landing)/contact-sales/page.tsx"),
    text("apps/web/app/(landing)/changelog/page.tsx"),
  ])
).join("\n");
const activeNetworkSurfaces = [
  remoteRuntimeDialog,
  helpLauncher,
  feedbackModal,
  desktopDaemonSettings,
  webExternalLinks,
  webReleaseSource,
].join("\n");

if (rootPackage.name !== "hivecrew") failures.push("root package name is not hivecrew");
if (desktopPackage.productName !== "HiveCrew") {
  failures.push("desktop productName is not HiveCrew");
}
if ("homepage" in desktopPackage || "repository" in desktopPackage) {
  failures.push("desktop manifest still declares inherited external ownership");
}

requireMatch(builder, /^appId: com\.hivecosm\.hivecrew$/m, "HiveCrew appId", failures);
requireMatch(builder, /^productName: HiveCrew$/m, "HiveCrew productName", failures);
requireMatch(builder, /^\s+- hivecrew$/m, "HiveCrew desktop protocol", failures);
forbidMatch(builder, /^publish:/m, "inherited desktop publish configuration", failures);
forbidMatch(builder, /^\s*artifactName: multica-desktop-/m, "legacy artifact name", failures);

requireMatch(runtime, /HIVE_CREW_LOCAL_ENDPOINTS/, "local-first runtime defaults", failures);
forbidMatch(runtime, /https:\/\/api\.multica\.ai|https:\/\/multica\.ai/, "legacy cloud runtime default", failures);
requireMatch(loader, /"\.hivecrew", "desktop\.json"/, "HiveCrew config path", failures);
requireMatch(loader, /"\.multica", "desktop\.json"/, "read-only legacy config fallback", failures);

requireMatch(bootstrap, /HIVECREW_CLI_RELEASE_BASE_URL/, "explicit HiveCrew CLI feed", failures);
forbidMatch(
  bootstrap,
  /github\.com\/multica-ai\/multica\/releases/,
  "legacy CLI release feed",
  failures,
);
requireMatch(desktopMain, /HIVE_CREW_DESKTOP_PROTOCOL/, "HiveCrew protocol registration", failures);
forbidMatch(desktopMain, /const PROTOCOL = "multica"/, "legacy primary protocol", failures);

requireMatch(webLayout, /HIVE_CREW_PRODUCT_NAME/, "HiveCrew web metadata", failures);
forbidMatch(webLayout, /www\.multica\.ai|@multica_hq/, "legacy web ownership metadata", failures);
requireMatch(webLogin, /HIVE_CREW_DESKTOP_PROTOCOL/, "HiveCrew login deep link", failures);
requireMatch(
  webAuthCallback,
  /HIVE_CREW_DESKTOP_PROTOCOL/,
  "HiveCrew OAuth deep link",
  failures,
);
forbidMatch(webLogin + webAuthCallback, /multica:\/\//, "legacy emitted deep link", failures);
requireMatch(webPublicHost, /HIVECREW_PUBLIC_APP_URL/, "configured public host", failures);
forbidMatch(webPublicHost, /multica\.ai/, "inherited public host", failures);
requireMatch(
  webExternalLinks,
  /NEXT_PUBLIC_HIVECREW_SOURCE_URL/,
  "configured source link",
  failures,
);
forbidMatch(
  webExternalLinks + webReleaseSource,
  /github\.com\/multica-ai\/multica|api\.github\.com\/repos\/multica-ai\/multica/,
  "inherited external repository",
  failures,
);
requireMatch(
  webReleaseSource,
  /HIVECREW_RELEASES_API_URL/,
  "configured HiveCrew release feed",
  failures,
);
forbidMatch(
  activeNetworkSurfaces,
  /https:\/\/(?:api\.)?multica\.ai|github\.com\/multica-ai\/multica|discord\.gg\/W8gYBn226t/,
  "inherited network ownership in active user surfaces",
  failures,
);
requireMatch(
  remoteRuntimeDialog,
  /HiveCrew remote runtime setup is unavailable until this deployment configures server_url and app_url/,
  "fail-closed remote runtime setup",
  failures,
);
forbidMatch(
  desktopRecovery + desktopRouteError + desktopRendererHtml + landingMetadata,
  /\bMultica\b/,
  "legacy visible product name",
  failures,
);

if (failures.length > 0) {
  console.error(JSON.stringify({ ok: false, failures }, null, 2));
  process.exitCode = 1;
} else {
  console.log(
    JSON.stringify(
      {
        ok: true,
        product: "HiveCrew",
        activeIdentityFilesChecked: 23,
        compatibilityPreserved: [
          "@multica package ABI",
          "legacy desktop config read fallback",
          "legacy deep-link parser",
        ],
      },
      null,
      2,
    ),
  );
}
