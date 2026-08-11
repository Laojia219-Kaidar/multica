// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance, ApiError } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { renderWithI18n } from "../test/i18n";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../navigation";
import { OrganizationPage } from "./organization-page";

const agentUuid = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const authority = {
  kind: "Employee",
  source_ref: "hivecosm://employees/DE-CEO-001",
  revision: "rev-1",
  content_digest: "sha256:abc",
  freshness: "current",
};

const organization = {
  schema_version: "hivecrew.organization.v1" as const,
  departments: [
    {
      department_id: "DE-DEPT-ENG",
      name: "Engineering",
      positions: [
        {
          position_id: "POS-ENG-LEAD",
          title: "Engineering Lead",
          appointments: [
            { appointment_id: "APP-1", employee_id: "DE-CEO-001", authority },
            { appointment_id: "APP-2", employee_id: "DE-ENG-002", authority },
          ],
          authority,
        },
      ],
      authority,
    },
  ],
  observed_at: "2026-08-12T00:00:00Z",
};

const rosterAvailable = {
  employee_id: "DE-CEO-001",
  display_name: "Coco",
  department_ref: "hivecosm://departments/DE-DEPT-ENG",
  position_ref: "hivecosm://positions/POS-ENG-LEAD",
  workforce_agent_id: "KT-002",
  hivecrew_agent_id: agentUuid,
  binding_state: "available" as const,
  authority,
};

const rosterConflict = {
  employee_id: "DE-ENG-002",
  display_name: "Turing",
  department_ref: "hivecosm://departments/DE-DEPT-ENG",
  position_ref: "hivecosm://positions/POS-ENG-LEAD",
  workforce_agent_id: "EXT-9",
  hivecrew_agent_id: null,
  binding_state: "multiple_active_conflict" as const,
  authority,
};

const dossier = (overrides: Record<string, unknown> = {}) => ({
  schema_version: "hivecrew.organization.v1",
  employee_id: "DE-CEO-001",
  display_name: "Coco",
  department_ref: "hivecosm://departments/DE-DEPT-ENG",
  position_ref: "hivecosm://positions/POS-ENG-LEAD",
  workforce_agent_id: "KT-002",
  hivecrew_agent_id: agentUuid,
  bindings: [
    {
      identity_binding_id: "BIND-001",
      employee_ref: "hivecosm://employees/DE-CEO-001",
      workforce_agent_id: "KT-002",
      agent_ref: `/api/agents/${agentUuid}`,
      active: true,
      authority,
    },
  ],
  binding_state: "available",
  local_agent: {
    id: agentUuid,
    name: "Coco",
    status: "idle",
    runtime_mode: "local",
    model: "deepseek-v4-flash",
    authority,
  },
  authority,
  ...overrides,
});

const mockOrg = vi.hoisted(() => vi.fn());
const mockRoster = vi.hoisted(() => vi.fn());
const mockDossier = vi.hoisted(() => vi.fn());
const compactRef = vi.hoisted(() => ({ current: false }));

vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: () => ({
    defaultLayout: undefined,
    onLayoutChanged: vi.fn(),
  }),
  usePanelRef: () => ({ current: { isCollapsed: () => false, collapse: vi.fn(), expand: vi.fn() } }),
}));

vi.mock("@multica/ui/components/ui/resizable", () => ({
  ResizablePanelGroup: ({ children }: { children: React.ReactNode }) => (
    <div>{children}</div>
  ),
  ResizablePanel: ({ children }: { children: React.ReactNode }) => (
    <div>{children}</div>
  ),
  ResizableHandle: () => null,
}));

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => false,
}));

vi.mock("./use-organization-compact", () => ({
  useOrganizationCompact: () => compactRef.current,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => ({
      organization: () => "/acme/organization",
      agentDetail: (id: string) => `/acme/agents/${id}`,
      chat: () => "/acme/chat",
    }),
    useRequiredWorkspaceSlug: () => "acme",
  };
});

function renderPage(searchParams: URLSearchParams) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  let view: ReturnType<typeof renderWithI18n> | null = null;
  let navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/organization",
    searchParams,
    getShareableUrl: (path) => path,
  };
  const replace = vi.fn((path: string) => {
    const url = new URL(path, "https://hivecrew.invalid");
    searchParams = new URLSearchParams(url.search);
    navigation = { ...navigation, searchParams };
    view?.rerender(
      <QueryClientProvider client={queryClient}>
        <NavigationProvider value={navigation}>
          <OrganizationPage />
        </NavigationProvider>
      </QueryClientProvider>,
    );
  });
  navigation = { ...navigation, replace };

  view = renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <NavigationProvider value={navigation}>
        <OrganizationPage />
      </NavigationProvider>
    </QueryClientProvider>,
  );
  return { replace, ...view };
}

beforeEach(() => {
  vi.clearAllMocks();
  compactRef.current = false;
  mockOrg.mockResolvedValue(organization);
  mockRoster.mockResolvedValue({
    schema_version: "hivecrew.organization.v1",
    items: [rosterAvailable, rosterConflict],
    total: 2,
    limit: 50,
    offset: 0,
  });
  mockDossier.mockResolvedValue(dossier());
  mockDossier.mockImplementation((_slug: string, employeeId: string) => {
    if (employeeId === "DE-ENG-002") {
      return Promise.resolve(
        dossier({
          employee_id: "DE-ENG-002",
          display_name: "Turing",
          workforce_agent_id: "EXT-9",
          hivecrew_agent_id: null,
          binding_state: "multiple_active_conflict",
          local_agent: null,
        }),
      );
    }
    return Promise.resolve(dossier());
  });
  setApiInstance({
    getCompanyOpsOrganization: mockOrg,
    listCompanyOpsEmployees: mockRoster,
    getCompanyOpsEmployee: mockDossier,
  } as unknown as ApiClient);
});

describe("OrganizationPage", () => {
  it("requests the org projection and renders the department tree", async () => {
    renderPage(new URLSearchParams());
    expect(await screen.findByText("Engineering")).toBeInTheDocument();
    expect(screen.getByText("Engineering Lead")).toBeInTheDocument();
    expect(screen.getByText("DE-CEO-001")).toBeInTheDocument();
    expect(screen.getByText("DE-ENG-002")).toBeInTheDocument();
    expect(mockOrg).toHaveBeenCalledTimes(1);
  });

  it("opens a dossier from the org tree and constructs exact links for available", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("Engineering");
    await userEvent.click(screen.getByText("DE-CEO-001"));
    expect(replace).toHaveBeenCalledWith("/acme/organization?employee=DE-CEO-001");
    const agentLink = await screen.findByRole("link", { name: "Open agent settings" });
    expect(agentLink).toHaveAttribute("href", `/acme/agents/${agentUuid}`);
    expect(screen.getByRole("link", { name: "Open chat to assign" })).toHaveAttribute(
      "href",
      `/acme/chat?agent=${agentUuid}`,
    );
  });

  it("switches to the roster tab and requests the roster with filters", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("Engineering");
    await userEvent.click(screen.getByRole("button", { name: /Employees roster/ }));
    expect(replace).toHaveBeenCalledWith("/acme/organization?tab=roster");
    expect(await screen.findByText("Coco")).toBeInTheDocument();
    expect(mockRoster).toHaveBeenCalledWith(
      "acme",
      expect.objectContaining({ limit: 50, offset: 0 }),
    );
  });

  it("shows the binding badge on roster rows and fail-closes the conflict row", async () => {
    const { replace } = renderPage(new URLSearchParams({ tab: "roster" }));
    await screen.findByText("Coco");
    expect(screen.getAllByText("Bound — assignable").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Binding conflict").length).toBeGreaterThan(0);
    // The conflict row must not construct dispatch links.
    await userEvent.click(screen.getByText("Turing"));
    expect(replace).toHaveBeenCalledWith(
      "/acme/organization?tab=roster&employee=DE-ENG-002",
    );
    await waitFor(() =>
      expect(
        screen.getAllByText(
          "Multiple active bindings exist. Resolve the conflict in HiveCosm before assigning.",
        ),
      ).toHaveLength(2),
    );
    expect(screen.queryByRole("link", { name: "Open chat to assign" })).not.toBeInTheDocument();
  });

  it("writes roster search and status filters into the URL and re-requests", async () => {
    const { replace } = renderPage(new URLSearchParams({ tab: "roster" }));
    await screen.findByText("Coco");
    const search = await screen.findByLabelText("Search employees");
    await userEvent.type(search, "coco");
    await waitFor(() =>
      expect(replace).toHaveBeenCalledWith("/acme/organization?tab=roster&q=coco"),
    );
    await screen.findByText("Coco");
    const status = await screen.findByLabelText("Binding state");
    await userEvent.selectOptions(status, "available");
    expect(replace).toHaveBeenCalledWith(
      "/acme/organization?tab=roster&q=coco&status=available",
    );
  });

  it("shows an explicit not-found for an invalid employee id", async () => {
    mockDossier.mockRejectedValue(new ApiError("missing", 404, "Not Found"));
    renderPage(new URLSearchParams({ employee: "DE-NOPE-000" }));
    expect(await screen.findByText("Employee not found")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open chat to assign" })).not.toBeInTheDocument();
  });

  it("shows a source-gap banner when the dossier request fails non-404", async () => {
    mockDossier.mockRejectedValue(new Error("authority unavailable"));
    renderPage(new URLSearchParams({ employee: "DE-CEO-001" }));
    expect(
      await screen.findByText("Organization authority unavailable"),
    ).toBeInTheDocument();
  });

  it("shows the loading skeleton while the dossier is pending", async () => {
    let release: (v: ReturnType<typeof dossier>) => void = () => {};
    mockDossier.mockImplementation(
      () =>
        new Promise<ReturnType<typeof dossier>>((resolve) => {
          release = resolve;
        }),
    );
    renderPage(new URLSearchParams({ employee: "DE-CEO-001" }));
    expect(await screen.findByText("Loading organization…")).toBeInTheDocument();
    release(dossier());
    await waitFor(() => expect(screen.getByText("Open agent settings")).toBeInTheDocument());
  });

  it("regression: normal organization load without selection keeps the select prompt", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("Engineering");
    expect(
      screen.getByText("Select an employee to view their dossier"),
    ).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
  });
});

describe("OrganizationPage compact (≤720px single column)", () => {
  beforeEach(() => {
    compactRef.current = true;
  });

  it("shows only the list on initial narrow-screen load", async () => {
    renderPage(new URLSearchParams());
    await screen.findByText("Engineering");
    expect(
      screen.queryByText("Select an employee to view their dossier"),
    ).not.toBeInTheDocument();
  });

  it("clicking an employee shows only the detail and keeps the exact URL", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("Engineering");
    await userEvent.click(screen.getByText("DE-CEO-001"));
    expect(replace).toHaveBeenCalledWith("/acme/organization?employee=DE-CEO-001");
    expect(await screen.findByRole("link", { name: "Open agent settings" })).toBeInTheDocument();
    expect(screen.queryByText("Engineering")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Back to organization" }),
    ).toBeInTheDocument();
  });

  it("clicking back clears the employee and restores the list", async () => {
    const { replace } = renderPage(
      new URLSearchParams({ employee: "DE-CEO-001" }),
    );
    await screen.findByRole("link", { name: "Open agent settings" });
    await userEvent.click(screen.getByRole("button", { name: "Back to organization" }));
    expect(replace).toHaveBeenCalledWith("/acme/organization");
    expect(await screen.findByText("Engineering")).toBeInTheDocument();
  });
});