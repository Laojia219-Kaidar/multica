import { mkdtemp, writeFile } from "fs/promises";
import { join } from "path";
import { tmpdir } from "os";
import { describe, expect, it, vi } from "vitest";

vi.mock("electron", () => ({
  app: {
    getPath: () => "/tmp/hivecrew-test-home",
  },
}));

import { loadRuntimeConfig } from "./runtime-config-loader";

describe("loadRuntimeConfig", () => {
  it("uses dev env and ignores desktop.json during electron-vite dev", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://prod.example.com" }),
    );

    await expect(
      loadRuntimeConfig({
        isDev: true,
        configPath,
        env: {
          apiUrl: "http://localhost:8080",
          wsUrl: "ws://localhost:8080/ws",
          appUrl: "http://localhost:3000",
        },
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "http://localhost:8080",
        wsUrl: "ws://localhost:8080/ws",
        appUrl: "http://localhost:3000",
      },
    });
  });

  it("uses local-only defaults when packaged and legacy configs are absent", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    await expect(
      loadRuntimeConfig({
        isDev: false,
        configPath: join(dir, "missing.json"),
        legacyConfigPath: join(dir, "legacy-missing.json"),
        env: {},
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "http://127.0.0.1:8080",
        wsUrl: "ws://127.0.0.1:8080/ws",
        appUrl: "http://127.0.0.1:3000",
      },
    });
  });

  it("parses a valid packaged desktop.json", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.example.com" }),
    );

    await expect(
      loadRuntimeConfig({ isDev: false, configPath, env: {} }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.example.com",
        wsUrl: "wss://api.example.com/ws",
        appUrl: "https://example.com",
      },
    });
  });

  it("fails closed when packaged desktop.json is invalid", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(configPath, "{");

    const result = await loadRuntimeConfig({ isDev: false, configPath, env: {} });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.message).toContain(configPath);
      expect(result.error.message).toContain("Invalid desktop runtime config JSON");
    }
  });

  it("reads the legacy config only when the HiveCrew config is absent", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "hivecrew.json");
    const legacyConfigPath = join(dir, "legacy.json");
    await writeFile(
      legacyConfigPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.legacy.example" }),
    );

    await expect(
      loadRuntimeConfig({
        isDev: false,
        configPath,
        legacyConfigPath,
        env: {},
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.legacy.example",
        wsUrl: "wss://api.legacy.example/ws",
        appUrl: "https://legacy.example",
      },
    });
  });

  it("prefers the HiveCrew config over an existing legacy config", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "hivecrew.json");
    const legacyConfigPath = join(dir, "legacy.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.hivecrew.example" }),
    );
    await writeFile(
      legacyConfigPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.legacy.example" }),
    );

    const result = await loadRuntimeConfig({
      isDev: false,
      configPath,
      legacyConfigPath,
      env: {},
    });

    expect(result.ok && result.config.apiUrl).toBe("https://api.hivecrew.example");
  });

  it("fails closed on an invalid HiveCrew config instead of using legacy data", async () => {
    const dir = await mkdtemp(join(tmpdir(), "hivecrew-desktop-config-"));
    const configPath = join(dir, "hivecrew.json");
    const legacyConfigPath = join(dir, "legacy.json");
    await writeFile(configPath, "{");
    await writeFile(
      legacyConfigPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.legacy.example" }),
    );

    const result = await loadRuntimeConfig({
      isDev: false,
      configPath,
      legacyConfigPath,
      env: {},
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.message).toContain(configPath);
  });
});
