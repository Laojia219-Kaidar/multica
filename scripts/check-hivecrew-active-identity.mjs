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

if (failures.length > 0) {
  console.error(JSON.stringify({ ok: false, failures }, null, 2));
  process.exitCode = 1;
} else {
  console.log(
    JSON.stringify(
      {
        ok: true,
        product: "HiveCrew",
        activeIdentityFilesChecked: 8,
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
