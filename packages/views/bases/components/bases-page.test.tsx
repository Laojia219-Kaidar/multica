// @vitest-environment jsdom
//
// Rendered coverage for the Bases registered-machine mapping repair
// (HIV-851): the only authority mapping an observed Runtime machine to a
// base is the formal registry `machine_title` — exact equality or the
// existing middle-dot suffix form `machine_title · device detail`.
// Parenthesis suffixes, arbitrary prefixes, substrings and keyword
// inference are rejected; unmatched machines stay visible as explicit
// unregistered bases without status statistics or management authority;
// migration targets are registered bases only; no hardcoded seven-name
// dependency remains.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import type { CockpitProjection } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enBases from "../../locales/en/bases.json";

interface CompanyBaseFixture {
  id: string;
  code: string;
  name: string;
  device: string;
  machine_title: string;
  agents: number;
}

interface BaseStatusFixture {
  machine_title: string;
  runtime_online: number;
  runtime_registered: number;
  employees: number;
  drained: boolean;
}

const runtimesRef = vi.hoisted(() => ({ current: [] as AgentRuntime[] }));
const agentsRef = vi.hoisted(() => ({ current: [] as Agent[] }));
const companyBasesRef = vi.hoisted(() => ({
  current: [] as CompanyBaseFixture[],
}));
const baseListRef = vi.hoisted(() => ({ current: [] as BaseStatusFixture[] }));
const cockpitRef = vi.hoisted(() => ({
  current: null as CockpitProjection | null,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: (wsId: string) => ({
    queryKey: ["runtimes", wsId],
    queryFn: () => Promise.resolve(runtimesRef.current),
  }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: () => Promise.resolve(agentsRef.current),
  }),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    getCompanyBases: () => Promise.resolve(companyBasesRef.current),
    listBases: () => Promise.resolve(baseListRef.current),
    getCockpitProjection: () => Promise.resolve(cockpitRef.current),
    setBaseOperationalMode: (machineTitle: string, mode: string) =>
      Promise.resolve({ machine_title: machineTitle, mode, agents_updated: 0 }),
    updateAgent: (agentId: string) => Promise.resolve({ id: agentId }),
  },
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { BasesPage } from "./bases-page";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Qwen (host)",
    custom_name: null,
    runtime_mode: "local",
    provider: "qwen",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    profile_id: null,
    last_seen_at: new Date().toISOString(),
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  };
}

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    name: "Ada",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: null }],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "qwen3",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function makeCompanyBase(overrides: Partial<CompanyBaseFixture>): CompanyBaseFixture {
  return {
    id: "base-01",
    code: "BASE-01",
    name: "中枢基地",
    device: "Mac mini",
    machine_title: "HiveCosm Mac mini",
    agents: 0,
    ...overrides,
  };
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, bases: enBases } }}
    >
      <QueryClientProvider client={queryClient}>
        <BasesPage />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  runtimesRef.current = [];
  agentsRef.current = [];
  companyBasesRef.current = [];
  baseListRef.current = [];
  cockpitRef.current = null;
});

describe("BasesPage registered machine mapping", () => {
  it("maps an exact registered machine_title to its base and keeps runtime details and management", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    baseListRef.current = [
      {
        machine_title: "HiveCosm DGX Spark",
        runtime_online: 1,
        runtime_registered: 1,
        employees: 0,
        drained: false,
      },
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        custom_name: "HiveCosm DGX Spark",
      }),
    ];

    renderPage();

    expect(await screen.findByText("底座基地")).toBeInTheDocument();
    expect(screen.getByText("HiveCosm DGX Spark")).toBeInTheDocument();
    // Registered cards retain the safe Runtime detail statistics …
    expect(screen.getByText("Runtimes online")).toBeInTheDocument();
    // … and the drain/resume management authority.
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Drain" })).toBeInTheDocument();
  });

  it("maps the middle-dot suffix form machine_title · device detail and keys status by the registered title", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-02",
        code: "BASE-02",
        name: "工程基地",
        device: "MBP M5X",
        machine_title: "HiveCrew MBP M5X",
      }),
    ];
    baseListRef.current = [
      {
        machine_title: "HiveCrew MBP M5X",
        runtime_online: 1,
        runtime_registered: 1,
        employees: 0,
        drained: true,
      },
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-mbp",
        daemon_id: "daemon-mbp",
        custom_name: "HiveCrew MBP M5X · 工程主机",
      }),
    ];

    renderPage();

    expect(await screen.findByText("工程基地")).toBeInTheDocument();
    expect(screen.getByText("HiveCrew MBP M5X · 工程主机")).toBeInTheDocument();
    // Drain state resolves through the registered machine_title even when
    // the observed machine carries the middle-dot suffix.
    expect(screen.getByText("Drained")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("rejects parenthesis suffixes as registry authority", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-fake",
        daemon_id: "daemon-fake",
        custom_name: "HiveCosm DGX Spark (备机)",
      }),
    ];

    renderPage();

    await expect(
      screen.findAllByText("HiveCosm DGX Spark (备机)"),
    ).resolves.toHaveLength(2); // card title + observed-machine subtitle
    // The parenthesized lookalike never claims the registered base name …
    expect(screen.queryByText("底座基地")).not.toBeInTheDocument();
    // … and is surfaced as an explicit unregistered base instead.
    expect(screen.getAllByText("未注册基地")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Drain" })).not.toBeInTheDocument();
  });

  it("keeps unmatched machines visible without registered-base status statistics or authority", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-stray",
        daemon_id: "daemon-stray",
        custom_name: "车间临时主机",
      }),
    ];

    renderPage();

    await expect(screen.findAllByText("车间临时主机")).resolves.toHaveLength(2);
    expect(screen.getByText("未注册基地")).toBeInTheDocument();
    // No registered-base status statistics.
    expect(screen.queryByText("Runtimes online")).not.toBeInTheDocument();
    expect(screen.queryByText("Status")).not.toBeInTheDocument();
    expect(screen.queryByText("Active")).not.toBeInTheDocument();
    expect(screen.queryByText("Drained")).not.toBeInTheDocument();
    // No drain/resume/migration authority.
    expect(screen.queryByRole("button", { name: "Drain" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("offers only registered bases as migration targets", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-01",
        code: "BASE-01",
        name: "中枢基地",
        device: "Mac mini",
        machine_title: "HiveCosm Mac mini",
      }),
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-mini",
        daemon_id: "daemon-mini",
        name: "HiveCosm Secure qwen-coding (HiveCosm Mac mini)",
        custom_name: "HiveCosm Mac mini",
      }),
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        name: "HiveCosm Secure qwen-coding (HiveCosm DGX Spark)",
        custom_name: "HiveCosm DGX Spark",
      }),
      makeRuntime({
        id: "runtime-stray",
        daemon_id: "daemon-stray",
        custom_name: "Mystery Box",
      }),
    ];
    agentsRef.current = [makeAgent({ id: "agent-1", runtime_id: "runtime-mini" })];

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /中枢基地/ }));

    const select = await screen.findByRole("combobox");
    const options = within(select)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(options).toContain("底座基地");
    // Unregistered machines are never migration targets …
    expect(options).not.toContain("Mystery Box");
    // … and the source machine is excluded.
    expect(options).not.toContain("中枢基地");
  });

  it("does not depend on the former hardcoded seven base names", async () => {
    companyBasesRef.current = [
      // A registry entry outside the former hardcoded seven still maps.
      makeCompanyBase({
        id: "base-x1",
        code: "BASE-X1",
        name: "轨道基地",
        device: "Orbital",
        machine_title: "Orbital Worker Node",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-orbital",
        daemon_id: "daemon-orbital",
        custom_name: "Orbital Worker Node",
      }),
      // A title starting with a former hardcoded prefix but absent from the
      // registry stays unregistered (prefix inference is gone).
      makeRuntime({
        id: "runtime-mini",
        daemon_id: "daemon-mini",
        custom_name: "HiveCosm Mac mini Deluxe",
      }),
    ];

    renderPage();

    expect(await screen.findByText("轨道基地")).toBeInTheDocument();
    expect(screen.queryByText("中枢基地")).not.toBeInTheDocument();
    expect(screen.getAllByText("未注册基地")).toHaveLength(1);
    await expect(
      screen.findAllByText("HiveCosm Mac mini Deluxe"),
    ).resolves.toHaveLength(2);
  });
});

/** Health dot of a cockpit projection row: the row's first span, located via the label text. */
function cockpitRowDot(label: string): Element {
  const labelEl = screen.getByText(label);
  const row = labelEl.closest("div.flex");
  if (!row) throw new Error(`cockpit row not found for label: ${label}`);
  const dot = row.querySelector("span");
  if (!dot) throw new Error(`cockpit dot not found for label: ${label}`);
  return dot;
}

describe("BasesPage semantic status visuals", () => {
  it("renders online status with the shared success token instead of raw emerald", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        custom_name: "HiveCosm DGX Spark",
      }),
    ];

    renderPage();

    const status = await screen.findByText("Online");
    expect(status.className).toContain("text-success");
    expect(status.className).not.toMatch(/emerald|red-/);
  });

  it("renders offline status with the shared destructive token instead of raw red", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        status: "offline",
        last_seen_at: "2026-08-01T00:00:00Z",
        custom_name: "HiveCosm DGX Spark",
      }),
    ];

    renderPage();

    const status = await screen.findByText("Offline");
    expect(status.className).toContain("text-destructive");
    expect(status.className).not.toMatch(/emerald|red-/);
  });

  it("styles the drain control with warning tone and the resume control with success tone", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-01",
        code: "BASE-01",
        name: "中枢基地",
        device: "Mac mini",
        machine_title: "HiveCosm Mac mini",
      }),
      makeCompanyBase({
        id: "base-02",
        code: "BASE-02",
        name: "工程基地",
        device: "MBP M5X",
        machine_title: "HiveCrew MBP M5X",
      }),
    ];
    baseListRef.current = [
      {
        machine_title: "HiveCosm Mac mini",
        runtime_online: 1,
        runtime_registered: 1,
        employees: 0,
        drained: false,
      },
      {
        machine_title: "HiveCrew MBP M5X",
        runtime_online: 1,
        runtime_registered: 1,
        employees: 0,
        drained: true,
      },
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-mini",
        daemon_id: "daemon-mini",
        custom_name: "HiveCosm Mac mini",
      }),
      makeRuntime({
        id: "runtime-mbp",
        daemon_id: "daemon-mbp",
        custom_name: "HiveCrew MBP M5X",
      }),
    ];

    renderPage();

    const drain = await screen.findByRole("button", { name: "Drain" });
    expect(drain.className).toContain("text-warning");
    expect(drain.className).toContain("border-warning/30");
    expect(drain.className).not.toMatch(/amber|emerald/);

    const resume = screen.getByRole("button", { name: "Resume" });
    expect(resume.className).toContain("text-success");
    expect(resume.className).toContain("border-success/30");
    expect(resume.className).not.toMatch(/amber|emerald/);
  });

  it("uses semantic tokens for cockpit projection health dots and unreachable sections", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        custom_name: "HiveCosm DGX Spark",
      }),
    ];
    cockpitRef.current = {
      fetched_at: "2026-08-23T10:00:00Z",
      cockpit_url: "http://hivecosm.local:1421",
      ok: true,
      sections: {
        health_surface: { ok: true },
        runtime_topology: { ok: false, error: "connection refused" },
      },
    };

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /底座基地/ }));
    expect(
      await screen.findByText("1421 驾驶舱投影（只读）"),
    ).toBeInTheDocument();

    expect(cockpitRowDot("健康面").className).toContain("bg-success");
    const failedDot = cockpitRowDot("运行拓扑");
    expect(failedDot.className).toContain("bg-destructive");
    expect(failedDot.className).not.toMatch(/emerald|red-/);

    const unreachable = screen.getByText("不可达");
    expect(unreachable.className).toContain("text-destructive");
  });

  it("keeps registered authority and unregistered fail-closed controls after the visual pass", async () => {
    companyBasesRef.current = [
      makeCompanyBase({
        id: "base-06",
        code: "BASE-06",
        name: "底座基地",
        device: "DGX",
        machine_title: "HiveCosm DGX Spark",
      }),
    ];
    runtimesRef.current = [
      makeRuntime({
        id: "runtime-dgx",
        daemon_id: "daemon-dgx",
        custom_name: "HiveCosm DGX Spark",
      }),
      makeRuntime({
        id: "runtime-stray",
        daemon_id: "daemon-stray",
        custom_name: "未登记主机",
      }),
    ];

    renderPage();

    // Registered base keeps its status statistics and drain authority …
    expect(await screen.findByText("底座基地")).toBeInTheDocument();
    expect(screen.getByText("Runtimes online")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Drain" })).toBeInTheDocument();
    // … while the unregistered machine stays fail-closed: visible, but with
    // no statistics and no drain/resume/migration controls.
    expect(screen.getByText("未注册基地")).toBeInTheDocument();
    const unregisteredTitles = screen.getAllByText("未登记主机");
    const card = unregisteredTitles[0]?.closest("div.rounded-lg");
    expect(card).not.toBeNull();
    expect(within(card as HTMLElement).queryByText("Runtimes online")).toBeNull();
    expect(within(card as HTMLElement).queryByRole("button", { name: "Drain" })).toBeNull();
    expect(within(card as HTMLElement).queryByRole("button", { name: "Resume" })).toBeNull();
    expect(within(card as HTMLElement).queryByRole("combobox")).toBeNull();
  });
});
