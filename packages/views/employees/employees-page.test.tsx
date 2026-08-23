// @vitest-environment jsdom
//
// Rendered coverage for the Employees-column Base authority repair (R6,
// HIV-856). Base joins the workforce-base-runtime read model ONLY by exact
// `employee_id` equality — never by name, position, department or truncated
// id. The complete response shape is validated before any row access; any
// structural doubt (non-array items, null/wrong-type/unknown rows, missing or
// whitespace-only Agent/Base evidence, duplicate or conflicting matches),
// plus query errors and refetch errors after a successful load, fail every
// card closed into the same generic `base_authority_gap` copy and never
// leave a stale verified Base. A well-formed response with no row for one
// Employee shows `base_not_verified` for that Employee only. Create/bind
// mutations keep their success/error toasts and really invalidate the
// employees query.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { workKeys } from "@multica/core/work-entry";
import { toast } from "sonner";
import { renderWithI18n } from "../test/i18n";

const EMP_ALICE = {
  id: "emp-alice-0001",
  name: "Alice",
  position: "Full-stack Engineer",
  department: "Engineering",
  agent_id: "agent-alice-0001",
  status: "active",
};
const EMP_BOB = {
  id: "emp-bob-0002",
  name: "Bob",
  position: undefined,
  department: undefined,
  agent_id: undefined,
  status: "draft",
};

const GAP_COPY = "Base authority unavailable";
const NOT_VERIFIED_COPY = "Base not verified";
const CHECKING_COPY = "Verifying base…";
const BASE_TITLE = "HiveCosm DGX Spark";

function workforceResponse(items: unknown, overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "hivecrew.workforce-base-runtime.v1",
    workspace_id: "ws-1",
    authority: { kind: "CompanyOps" },
    items,
    ...overrides,
  };
}

function row(overrides: Record<string, unknown> = {}) {
  return {
    employee_id: EMP_ALICE.id,
    workforce_agent_id: EMP_ALICE.agent_id,
    base_machine_title: BASE_TITLE,
    ...overrides,
  };
}

const mockListEmployees = vi.hoisted(() => vi.fn());
const mockCreateEmployee = vi.hoisted(() => vi.fn());
const mockUpdateEmployeeBinding = vi.hoisted(() => vi.fn());
const mockWorkforce = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { EmployeesPage } from "./employees-page";

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const view = renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <EmployeesPage />
    </QueryClientProvider>,
  );
  return { queryClient, ...view };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockListEmployees.mockResolvedValue([EMP_ALICE, EMP_BOB]);
  mockCreateEmployee.mockResolvedValue({ id: "emp-new-0003" });
  mockUpdateEmployeeBinding.mockImplementation((id: string, data: { status?: string }) =>
    Promise.resolve({ id, status: data.status ?? "draft" }),
  );
  mockWorkforce.mockResolvedValue(workforceResponse([]));
  setApiInstance({
    listEmployees: mockListEmployees,
    createEmployee: mockCreateEmployee,
    updateEmployeeBinding: mockUpdateEmployeeBinding,
    listWorkforceBaseRuntime: mockWorkforce,
  } as unknown as ApiClient);
});

async function cards() {
  await screen.findByText(/Alice/);
  return {
    alice: screen.getByText(/Alice/).closest("li") as HTMLElement,
    bob: screen.getByText("Bob").closest("li") as HTMLElement,
  };
}

describe("EmployeesPage Base authority join", () => {
  it("verifies a Base only through an exact employee_id match", async () => {
    mockWorkforce.mockResolvedValue(workforceResponse([row()]));

    renderPage();
    const { alice, bob } = await cards();

    expect(within(alice).getByText(BASE_TITLE)).toBeInTheDocument();
    expect(within(alice).getByText("Base")).toBeInTheDocument();
    expect(within(bob).queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(within(bob).getByText(NOT_VERIFIED_COPY)).toBeInTheDocument();
    expect(screen.queryByText(GAP_COPY)).not.toBeInTheDocument();
  });

  it("never joins by truncated id, name, position or department", async () => {
    mockWorkforce.mockResolvedValue(
      workforceResponse([
        // Truncated-id prefix of Alice's employee_id.
        row({ employee_id: "emp-alice", workforce_agent_id: "wf-1", base_machine_title: "Lookalike Base" }),
        // Name / position / department lookalikes for Bob.
        { employee_id: "Bob", workforce_agent_id: "wf-2", base_machine_title: "Lookalike Base" },
        { employee_id: "Engineering", workforce_agent_id: "wf-3", base_machine_title: "Lookalike Base" },
      ]),
    );

    renderPage();
    const { alice, bob } = await cards();

    expect(within(alice).getByText(NOT_VERIFIED_COPY)).toBeInTheDocument();
    expect(within(bob).getByText(NOT_VERIFIED_COPY)).toBeInTheDocument();
    expect(screen.queryByText("Lookalike Base")).not.toBeInTheDocument();
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(GAP_COPY)).not.toBeInTheDocument();
  });

  it("shows base_not_verified for an isolated unmatched employee without a global gap", async () => {
    mockWorkforce.mockResolvedValue(workforceResponse([row()]));

    renderPage();
    const { alice, bob } = await cards();

    // The verified row for Alice stays visible in the same valid response …
    expect(within(alice).getByText(BASE_TITLE)).toBeInTheDocument();
    // … while clean-unmatched Bob gets the isolated not-verified state.
    expect(within(bob).getByText(NOT_VERIFIED_COPY)).toBeInTheDocument();
    expect(screen.queryByText(GAP_COPY)).not.toBeInTheDocument();
  });

  it("fails closed to the generic gap when Agent evidence is whitespace-only", async () => {
    mockWorkforce.mockResolvedValue(
      workforceResponse([row({ workforce_agent_id: "   " })]),
    );

    renderPage();
    await cards();

    expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
  });

  it("fails closed to the generic gap when Base evidence is missing", async () => {
    mockWorkforce.mockResolvedValue(
      workforceResponse([row({ base_machine_title: undefined })]),
    );

    renderPage();
    await cards();

    expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
    // Every Employee card is preserved.
    expect(screen.getByText(/Alice/)).toBeInTheDocument();
    expect(screen.getByText("Bob")).toBeInTheDocument();
  });

  it("fails closed when items is not an array or the schema version is unknown", async () => {
    mockWorkforce.mockResolvedValue(workforceResponse({ not: "an-array" }));

    const first = renderPage();
    expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
    first.unmount();

    mockWorkforce.mockResolvedValue(
      workforceResponse([row()], { schema_version: "hivecrew.workforce-base-runtime.v0" }),
    );
    renderPage();
    expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
  });

  it("fails closed on null, wrong-type and unknown rows", async () => {
    for (const badItems of [
      [null],
      ["a-string-row"],
      [{ something: "else" }],
    ]) {
      mockWorkforce.mockResolvedValueOnce(workforceResponse(badItems));
      const { unmount } = renderPage();
      expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
      expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
      expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
      unmount();
    }
  });

  it("fails every card closed when one row is malformed next to a valid row", async () => {
    mockWorkforce.mockResolvedValue(
      workforceResponse([
        row(),
        { employee_id: EMP_BOB.id, workforce_agent_id: 12345, base_machine_title: "Other Base" },
      ]),
    );

    renderPage();
    const { alice, bob } = await cards();

    // The malformed row poisons the whole read model: even the Employee with
    // a superficially valid row must not show a verified Base.
    expect(await within(alice).findByText(GAP_COPY)).toBeInTheDocument();
    expect(within(bob).getByText(GAP_COPY)).toBeInTheDocument();
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
  });

  it("fails closed on duplicate or conflicting matches for one employee", async () => {
    mockWorkforce.mockResolvedValue(
      workforceResponse([row(), row({ base_machine_title: "Conflicting Base" })]),
    );

    renderPage();
    expect(await screen.findAllByText(GAP_COPY)).toHaveLength(2);
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText("Conflicting Base")).not.toBeInTheDocument();
  });

  it("keeps every employee card and shows the gap on an initial query error", async () => {
    mockWorkforce.mockRejectedValue(new Error("source down"));

    renderPage();
    const { alice, bob } = await cards();

    expect(await within(alice).findByText(GAP_COPY)).toBeInTheDocument();
    expect(within(bob).getByText(GAP_COPY)).toBeInTheDocument();
    // Employee cards survive the source failure …
    expect(screen.getByText(/Alice/)).toBeInTheDocument();
    expect(screen.getByText("Bob")).toBeInTheDocument();
    // … and neither the not-verified state, a cached Base nor the raw error
    // is displayed.
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(/source down/)).not.toBeInTheDocument();
  });

  it("fails closed on a refetch error and never keeps the stale verified Base", async () => {
    mockWorkforce
      .mockResolvedValueOnce(workforceResponse([row()]))
      .mockRejectedValueOnce(new Error("refetch down"));
    const { queryClient } = renderPage();
    const { alice } = await cards();

    expect(await within(alice).findByText(BASE_TITLE)).toBeInTheDocument();

    await queryClient.invalidateQueries({
      queryKey: workKeys.workforceBaseRuntime("ws-1"),
    });

    expect(await within(alice).findByText(GAP_COPY)).toBeInTheDocument();
    // TanStack Query still retains the stale payload, yet the stale verified
    // Base must not survive the source failure.
    expect(queryClient.getQueryData(workKeys.workforceBaseRuntime("ws-1"))).toBeDefined();
    expect(screen.queryByText(BASE_TITLE)).not.toBeInTheDocument();
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
  });

  it("shows the checking state while the Base authority is still loading", async () => {
    mockWorkforce.mockReturnValue(new Promise(() => {}));

    renderPage();
    expect(await screen.findAllByText(CHECKING_COPY)).toHaveLength(2);
    expect(screen.queryByText(GAP_COPY)).not.toBeInTheDocument();
    expect(screen.queryByText(NOT_VERIFIED_COPY)).not.toBeInTheDocument();
  });
});

describe("EmployeesPage create and binding mutations", () => {
  it("creates an employee with success toast, real employees-query invalidation and form reset", async () => {
    renderPage();
    await cards();

    await userEvent.type(screen.getByPlaceholderText("Name"), "Carol");
    await userEvent.type(screen.getByPlaceholderText("Position (e.g. full-stack engineer)"), "SRE");
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(mockCreateEmployee).toHaveBeenCalledWith({
        name: "Carol",
        position: "SRE",
        department: undefined,
      }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Employee identity created"),
    );
    // The employees query is actually invalidated: the list is refetched,
    // not merely claimed invalidated.
    await waitFor(() => expect(mockListEmployees).toHaveBeenCalledTimes(2));
    expect(toast.error).not.toHaveBeenCalled();
    expect((screen.getByPlaceholderText("Name") as HTMLInputElement).value).toBe("");
    expect((screen.getByPlaceholderText("Position (e.g. full-stack engineer)") as HTMLInputElement).value).toBe("");
  });

  it("reports a create failure with an error toast and no false success", async () => {
    mockCreateEmployee.mockRejectedValue(new Error("create denied"));
    renderPage();
    await cards();

    await userEvent.type(screen.getByPlaceholderText("Name"), "Carol");
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Creation failed"));
    expect(toast.success).not.toHaveBeenCalled();
    // The failed create never invalidates the employees list.
    expect(mockListEmployees).toHaveBeenCalledTimes(1);
    expect((screen.getByPlaceholderText("Name") as HTMLInputElement).value).toBe("Carol");
  });

  it("binds a status with success toast and real employees-query invalidation", async () => {
    renderPage();
    const { alice } = await cards();

    fireEvent.change(within(alice).getByRole("combobox"), {
      target: { value: "canary" },
    });

    await waitFor(() =>
      expect(mockUpdateEmployeeBinding).toHaveBeenCalledWith(EMP_ALICE.id, {
        agent_id: EMP_ALICE.agent_id,
        status: "canary",
      }),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Onboarded"));
    await waitFor(() => expect(mockListEmployees).toHaveBeenCalledTimes(2));
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("reports a binding failure with an error toast and no false success", async () => {
    mockUpdateEmployeeBinding.mockRejectedValue(new Error("bind denied"));
    renderPage();
    const { bob } = await cards();

    fireEvent.change(within(bob).getByRole("combobox"), {
      target: { value: "active" },
    });

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Onboarding failed"));
    await waitFor(() =>
      expect(mockUpdateEmployeeBinding).toHaveBeenCalledWith(EMP_BOB.id, {
        agent_id: "",
        status: "active",
      }),
    );
    expect(toast.success).not.toHaveBeenCalled();
    expect(mockListEmployees).toHaveBeenCalledTimes(1);
  });
});
