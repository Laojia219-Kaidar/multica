// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Agent, AgentRuntime, SquadMember } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSquads from "../../locales/en/squads.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";
import { buildSquadBaseProjection } from "./squad-base-projection";
import { SquadBaseDistributionTab } from "./squad-detail-page";

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
  useWorkspacePaths: () => ({
    agentDetail: (agentId: string) => `/acme/agents/${agentId}`,
  }),
}));

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Ada | Full-stack engineer",
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
  model: "glm-5",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const member: SquadMember = {
  id: "member-1",
  squad_id: "squad-1",
  member_type: "agent",
  member_id: "agent-1",
  role: "Full-stack engineer",
  created_at: "2026-08-13T00:00:00Z",
};

const runtime: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "ws-1",
  daemon_id: "daemon-1",
  name: "Codex (HiveCrew MBP M5X)",
  custom_name: "Engineering Runtime",
  runtime_mode: "local",
  provider: "codex",
  launch_header: "",
  status: "online",
  device_info: "HiveCrew MBP M5X · macOS (arm64)",
  metadata: {},
  owner_id: "user-1",
  visibility: "private",
  profile_id: null,
  last_seen_at: "2026-08-13T00:00:00Z",
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
};

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/squads/squad-1",
  searchParams: new URLSearchParams(),
  getShareableUrl: (path) => path,
};

function renderTab({ loading = false, error = false } = {}) {
  const projection = buildSquadBaseProjection({
    members: [member],
    agents: [agent],
    runtimes: [runtime],
    memberStatusById: new Map([
      [
        "agent-1",
        {
          member_type: "agent",
          member_id: "agent-1",
          status: "working",
          active_issues: [],
          last_active_at: null,
        },
      ],
    ]),
  });

  render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, squads: enSquads } }}
    >
      <NavigationProvider value={navigation}>
        <SquadBaseDistributionTab
          projection={projection}
          loading={loading}
          error={error}
        />
      </NavigationProvider>
    </I18nProvider>,
  );
}

describe("SquadBaseDistributionTab", () => {
  it("renders observed base, runtime, model, status, and the authority boundary", () => {
    renderTab();

    expect(screen.getByText("Current execution-base distribution")).toBeInTheDocument();
    expect(
      screen.getByText(/observed execution-base projection/i),
    ).toBeInTheDocument();
    expect(screen.getByText("HiveCrew MBP M5X")).toBeInTheDocument();
    expect(screen.getByText("Ada | Full-stack engineer")).toBeInTheDocument();
    expect(screen.getByText(/Engineering Runtime \(Codex\)/)).toBeInTheDocument();
    expect(screen.getByText(/glm-5/)).toBeInTheDocument();
    expect(screen.getAllByText("Working")).toHaveLength(2);
    expect(screen.getByText("Runtime online")).toBeInTheDocument();
  });

  it("fails closed when Runtime data cannot be read", () => {
    renderTab({ error: true });

    expect(
      screen.getByText("Base and Runtime information is unavailable"),
    ).toBeInTheDocument();
    expect(screen.queryByText("HiveCrew MBP M5X")).not.toBeInTheDocument();
    expect(screen.queryByText("glm-5")).not.toBeInTheDocument();
  });
});
