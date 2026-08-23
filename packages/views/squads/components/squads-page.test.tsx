// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { Agent, MemberWithUser, Squad } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSquads from "../../locales/en/squads.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";
import { SquadsPage } from "./squads-page";

// ---------------------------------------------------------------------------
// Mutable state — tests adjust these before rendering; mocks read them.
// ---------------------------------------------------------------------------

let mockUser: { id: string; name: string } | null = {
  id: "user-admin",
  name: "Admin User",
};

const queryState: {
  squads: { data?: Squad[]; isLoading?: boolean; error?: unknown; refetch?: () => void };
  agents: { data?: Agent[] };
  members: { data?: MemberWithUser[] };
} = {
  squads: {},
  agents: {},
  members: {},
};

const storeState: {
  scope: "mine" | "all";
  sortField: string;
  sortDirection: string;
  hiddenColumns: string[];
  filters: { leaders: string[]; creators: string[] };
} = {
  scope: "all",
  sortField: "name",
  sortDirection: "asc",
  hiddenColumns: [],
  filters: { leaders: [], creators: [] },
};

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

vi.mock("@multica/ui/components/common/actor-avatar", () => ({
  ActorAvatar: ({ name }: { name: string }) => (
    <span data-testid={`actor-${name}`} />
  ),
}));

vi.mock("../../common/hover-check", () => ({
  FILTER_ITEM_CLASS: "",
  HoverCheck: () => null,
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
  useWorkspacePaths: () => ({
    squadDetail: (id: string) => `/acme/squads/${id}`,
  }),
}));

vi.mock("@multica/core/modals", () => ({
  useModalStore: { getState: () => ({ open: vi.fn() }) },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: typeof mockUser }) => unknown) =>
    selector({ user: mockUser }),
}));

vi.mock("../../navigation", () => ({
  useRowLink: () => () => ({ onClick: vi.fn(), href: "#" }),
  NavigationProvider: ({
    children,
  }: {
    children: React.ReactNode;
  }) => <>{children}</>,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: readonly unknown[] }) => {
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("squads")) return queryState.squads;
    if (key.includes("agents")) return { data: queryState.agents.data ?? [] };
    if (key.includes("members"))
      return { data: queryState.members.data ?? [] };
    return { data: [] };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  squadListOptions: () => ({ queryKey: ["squads", "ws-1"] }),
  agentListOptions: () => ({ queryKey: ["agents", "ws-1"] }),
  memberListOptions: () => ({ queryKey: ["members", "ws-1"] }),
  workspaceKeys: { squads: (id: string) => ["squads", id] },
}));

vi.mock("@multica/core/api", () => ({
  api: { deleteSquad: vi.fn() },
}));

vi.mock("@multica/core/workspace/avatar-url", () => ({
  resolvePublicFileUrl: (url: string) => url,
}));

vi.mock("@multica/core/squads/stores", () => ({
  SQUAD_SCOPES: ["mine", "all"] as const,
  SQUAD_DEFAULT_HIDDEN_COLUMNS: [] as string[],
  useSquadsViewStore: (selector: (s: typeof storeState) => unknown) =>
    selector(storeState),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const agentAda: Agent = {
  id: "agent-ada",
  workspace_id: "ws-1",
  runtime_id: "rt-1",
  name: "Ada",
  description: "Full-stack engineer",
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
  owner_id: "user-admin",
  skills: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const adminMember: MemberWithUser = {
  user_id: "user-admin",
  workspace_id: "ws-1",
  role: "owner",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as unknown as MemberWithUser;

function makeSquad(overrides: Partial<Squad> = {}): Squad {
  return {
    id: "squad-1",
    workspace_id: "ws-1",
    name: "Engineering",
    description: "Core engineering team",
    avatar_url: null,
    leader_id: "agent-ada",
    creator_id: "user-admin",
    member_count: 2,
    member_preview: [],
    created_at: "2026-01-15T00:00:00Z",
    updated_at: "2026-01-15T00:00:00Z",
    ...overrides,
  } as Squad;
}

const navigation = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  searchParams: new URLSearchParams(),
  getShareableUrl: (path: string) => path,
} as unknown as NavigationAdapter;

function renderPage() {
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, squads: enSquads } }}
    >
      <NavigationProvider value={navigation}>
        <SquadsPage />
      </NavigationProvider>
    </I18nProvider>,
  );
}

function resetDefaults() {
  mockUser = { id: "user-admin", name: "Admin User" };
  queryState.squads = {};
  queryState.agents = { data: [agentAda] };
  queryState.members = { data: [adminMember] };
  storeState.scope = "all";
  storeState.sortField = "name";
  storeState.sortDirection = "asc";
  storeState.hiddenColumns = [];
  storeState.filters = { leaders: [], creators: [] };
}

// ---------------------------------------------------------------------------
// Visual-state contract
// ---------------------------------------------------------------------------

describe("SquadsPage visual-state contract", () => {
  beforeEach(() => {
    resetDefaults();
  });

  it("renders a loading skeleton while the squad list is pending", () => {
    queryState.squads = { isLoading: true };
    renderPage();
    // The page header is always present, but no data, empty, or error text
    expect(screen.queryByText("No squads yet")).not.toBeInTheDocument();
    expect(screen.queryByText("Engineering")).not.toBeInTheDocument();
    expect(screen.queryByText("Failed to load squads")).not.toBeInTheDocument();
  });

  it("renders the shared empty state with title and description when no squads exist", () => {
    queryState.squads = { data: [], isLoading: false };
    renderPage();
    expect(screen.getByText("No squads yet")).toBeInTheDocument();
    expect(
      screen.getByText(/create a squad to group agents/i),
    ).toBeInTheDocument();
    // Two "New Squad" buttons: header + empty-state action
    expect(
      screen.getAllByRole("button", { name: /new squad/i }),
    ).toHaveLength(2);
  });

  it("renders a destructive error state when the squad list fails", () => {
    queryState.squads = {
      data: undefined,
      isLoading: false,
      error: new Error("Network timeout"),
      refetch: vi.fn(),
    };
    renderPage();
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("Failed to load squads")).toBeInTheDocument();
    expect(screen.getByText("Network timeout")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /try again/i }),
    ).toBeInTheDocument();
  });

  it("renders squad rows in the list grid when data is available", () => {
    queryState.squads = { data: [makeSquad()], isLoading: false };
    renderPage();
    expect(screen.getByText("Engineering")).toBeInTheDocument();
    expect(screen.getByText("Core engineering team")).toBeInTheDocument();
  });

  it("renders the no-matches state when scope filters exclude all rows", () => {
    const squad = makeSquad({ creator_id: "user-other" });
    queryState.squads = { data: [squad], isLoading: false };
    // "mine" scope + user is not the creator → 0 visible rows
    mockUser = { id: "user-nobody", name: "Nobody" };
    queryState.members = {
      data: [
        {
          user_id: "user-nobody",
          workspace_id: "ws-1",
          role: "member",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        } as unknown as MemberWithUser,
      ],
    };
    storeState.scope = "mine";

    renderPage();
    expect(screen.getByText("No squads match")).toBeInTheDocument();
    expect(
      screen.getByText(/adjusting your filters/i),
    ).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Permission gates
// ---------------------------------------------------------------------------

describe("SquadsPage permission gates", () => {
  beforeEach(() => {
    resetDefaults();
  });

  it("shows row actions for a workspace admin on every squad", () => {
    // Admin, but squad was created by someone else
    mockUser = { id: "user-admin", name: "Admin" };
    queryState.members = { data: [adminMember] };
    queryState.squads = {
      data: [makeSquad({ creator_id: "user-someone-else" })],
      isLoading: false,
    };
    renderPage();
    expect(
      screen.getByRole("button", { name: /squad actions/i }),
    ).toBeInTheDocument();
  });

  it("shows row actions for the squad creator even when not admin", () => {
    mockUser = { id: "user-creator", name: "Creator" };
    queryState.members = {
      data: [
        {
          user_id: "user-creator",
          workspace_id: "ws-1",
          role: "member",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        } as unknown as MemberWithUser,
      ],
    };
    queryState.squads = {
      data: [makeSquad({ creator_id: "user-creator" })],
      isLoading: false,
    };
    renderPage();
    expect(
      screen.getByRole("button", { name: /squad actions/i }),
    ).toBeInTheDocument();
  });

  it("hides row actions from a non-admin who is not the creator", () => {
    mockUser = { id: "user-viewer", name: "Viewer" };
    queryState.members = {
      data: [
        {
          user_id: "user-viewer",
          workspace_id: "ws-1",
          role: "member",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        } as unknown as MemberWithUser,
      ],
    };
    queryState.squads = {
      data: [makeSquad({ creator_id: "user-someone-else" })],
      isLoading: false,
    };
    renderPage();
    expect(
      screen.queryByRole("button", { name: /squad actions/i }),
    ).not.toBeInTheDocument();
  });
});
