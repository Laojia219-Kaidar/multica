// @vitest-environment jsdom
//
// Focused tests for the Workrooms page UI consistency R3:
// - Uses shared Button, Input, Card components instead of raw elements
// - Skeleton loading state instead of plain text
// - Empty state rendered via CollectionPageState
// - Error state with retry action
// - Create mutation: success toast, query invalidation, form reset
// - Create mutation: error toast, no invalidation, form values preserved
// - Safe shortened ID references (8-char prefix) for id/issue/project/work_order

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { toast } from "sonner";
import { renderWithI18n } from "../test/i18n";

const WR_1 = {
  id: "wr-alice-0001",
  name: "Alpha Sprint",
  issue_id: "issue-aaa-001",
  project_id: "proj-aaa-001",
  work_order_id: "wo-aaa-00001",
  created_by: "agent-1",
};

const WR_2 = {
  id: "wr-bob-00002",
  name: "Beta Workspace",
  issue_id: undefined,
  project_id: undefined,
  work_order_id: undefined,
  created_by: "agent-2",
};

const mockListWorkrooms = vi.hoisted(() => vi.fn());
const mockCreateWorkroom = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { WorkroomsPage } from "./workrooms-page";

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const view = renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <WorkroomsPage />
    </QueryClientProvider>,
  );
  return { queryClient, ...view };
}

function bySlot(slot: string) {
  return document.querySelector(`[data-slot="${slot}"]`) as HTMLElement | null;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockListWorkrooms.mockResolvedValue([WR_1, WR_2]);
  mockCreateWorkroom.mockResolvedValue({ id: "wr-new-0003" });
  setApiInstance({
    listWorkrooms: mockListWorkrooms,
    createWorkroom: mockCreateWorkroom,
  } as unknown as ApiClient);
});

describe("WorkroomsPage states", () => {
  it("renders a skeleton list while loading", () => {
    mockListWorkrooms.mockReturnValue(new Promise(() => {}));
    renderPage();

    const skeleton = bySlot("workroom-list-skeleton");
    expect(skeleton).toBeInTheDocument();
    expect(skeleton!.querySelectorAll("li")).toHaveLength(3);
    expect(screen.queryByText("Alpha Sprint")).not.toBeInTheDocument();
    expect(screen.queryByText("暂无协作空间")).not.toBeInTheDocument();
  });

  it("shows the empty state when there are no workrooms", async () => {
    mockListWorkrooms.mockResolvedValue([]);
    renderPage();

    expect(await screen.findByText("暂无协作空间")).toBeInTheDocument();
    expect(
      screen.getByText("创建第一个协作空间，开启人机协作。"),
    ).toBeInTheDocument();
  });

  it("shows the error state with a retry action on query failure", async () => {
    mockListWorkrooms.mockRejectedValue(new Error("list failed"));
    renderPage();

    expect(await screen.findByText("加载失败")).toBeInTheDocument();
    expect(screen.getByText("无法加载协作空间列表，请重试。")).toBeInTheDocument();

    const retryButton = screen.getByRole("button", { name: "重试" });
    expect(retryButton).toBeInTheDocument();

    expect(mockListWorkrooms).toHaveBeenCalledTimes(1);
    fireEvent.click(retryButton);
    await waitFor(() => expect(mockListWorkrooms).toHaveBeenCalledTimes(2));
  });
});

describe("WorkroomsPage list rendering", () => {
  it("renders each workroom with its name and shortened id reference", async () => {
    renderPage();

    expect(await screen.findByText("Alpha Sprint")).toBeInTheDocument();
    expect(screen.getByText("Beta Workspace")).toBeInTheDocument();
  });

  it("shortens id, issue, project and work_order references to 8 chars", async () => {
    renderPage();

    const alpha = (await screen.findByText("Alpha Sprint")).closest("li");
    expect(alpha).not.toBeNull();

    const meta = within(alpha!).getByText(/^id /);
    // id: "wr-alice-0001".slice(0, 8) = "wr-alice"
    expect(meta.textContent).toContain("id wr-alice");
    // issue_id: "issue-aaa-001".slice(0, 8) = "issue-aa"
    expect(meta.textContent).toContain("议题 issue-aa");
    // project_id: "proj-aaa-001".slice(0, 8) = "proj-aaa"
    expect(meta.textContent).toContain("项目 proj-aaa");
    // work_order_id: "wo-aaa-00001".slice(0, 8) = "wo-aaa-0"
    expect(meta.textContent).toContain("工单 wo-aaa-0");
  });

  it("shows only the id meta line for a workroom with no bound references", async () => {
    renderPage();

    const beta = (await screen.findByText("Beta Workspace")).closest("li");
    expect(beta).not.toBeNull();

    const meta = within(beta!).getByText(/^id /);
    // id: "wr-bob-00002".slice(0, 8) = "wr-bob-0"
    expect(meta.textContent).toContain("id wr-bob-0");
    expect(meta.textContent).not.toContain("议题");
    expect(meta.textContent).not.toContain("项目");
    expect(meta.textContent).not.toContain("工单");
  });

  it("uses the Card component for the list container", async () => {
    renderPage();

    await screen.findByText("Alpha Sprint");
    const listCard = bySlot("workroom-list-card");
    expect(listCard).toBeInTheDocument();
    // The Card component renders with data-slot="card" — confirm the slot
    // value we set on the Card maps to a rendered element.
    expect(listCard!.tagName).toBe("DIV");
  });

  it("renders each workroom item with the surface token classes", async () => {
    renderPage();

    const alpha = await screen.findByText("Alpha Sprint");
    const item = alpha.closest("li");
    expect(item).toHaveClass("bg-surface");
    expect(item).toHaveClass("border-surface-border");
  });
});

describe("WorkroomsPage create form", () => {
  it("uses shared Input and Button components", async () => {
    renderPage();
    await screen.findByText("Alpha Sprint");

    const nameInput = screen.getByPlaceholderText("空间名称");
    expect(nameInput.tagName).toBe("INPUT");
    expect(nameInput.dataset.slot).toBe("input");

    const issueInput = screen.getByPlaceholderText("议题 ID（可选，8 位前缀）");
    expect(issueInput.dataset.slot).toBe("input");

    const createButton = screen.getByRole("button", { name: "创建" });
    expect(createButton.dataset.slot).toBe("button");
  });

  it("disables the create button when name is empty", async () => {
    renderPage();
    await screen.findByText("Alpha Sprint");

    const button = screen.getByRole("button", { name: "创建" });
    expect(button).toBeDisabled();

    await userEvent.type(screen.getByPlaceholderText("空间名称"), "Test Space");
    expect(button).toBeEnabled();
  });

  it("creates a workroom with success toast, query invalidation and form reset", async () => {
    renderPage();
    await screen.findByText("Alpha Sprint");

    await userEvent.type(screen.getByPlaceholderText("空间名称"), "Gamma Room");
    await userEvent.type(
      screen.getByPlaceholderText("议题 ID（可选，8 位前缀）"),
      "issue-new-",
    );
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    await waitFor(() =>
      expect(mockCreateWorkroom).toHaveBeenCalledWith({
        name: "Gamma Room",
        issue_id: "issue-new-",
      }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("协作空间已创建"),
    );
    await waitFor(() => expect(mockListWorkrooms).toHaveBeenCalledTimes(2));
    expect(toast.error).not.toHaveBeenCalled();
    expect(
      (screen.getByPlaceholderText("空间名称") as HTMLInputElement).value,
    ).toBe("");
    expect(
      (screen.getByPlaceholderText("议题 ID（可选，8 位前缀）") as HTMLInputElement)
        .value,
    ).toBe("");
  });

  it("disables inputs and button while create is pending", async () => {
    let resolveCreate: (value: { id: string }) => void = () => {};
    mockCreateWorkroom.mockReturnValue(
      new Promise<{ id: string }>((res) => {
        resolveCreate = res;
      }),
    );

    renderPage();
    await screen.findByText("Alpha Sprint");

    await userEvent.type(screen.getByPlaceholderText("空间名称"), "Delta Space");
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    const createCard = bySlot("create-workroom-card");
    const button = createCard!.querySelector('[data-slot="button"]') as HTMLButtonElement;

    await waitFor(() => {
      expect(button.disabled).toBe(true);
    });
    expect(screen.getByPlaceholderText("空间名称")).toBeDisabled();
    expect(
      screen.getByPlaceholderText("议题 ID（可选，8 位前缀）"),
    ).toBeDisabled();

    resolveCreate({ id: "wr-delta-01" });
  });

  it("omits issue_id when the issue field is blank", async () => {
    renderPage();
    await screen.findByText("Alpha Sprint");

    await userEvent.type(screen.getByPlaceholderText("空间名称"), "Epsilon Lab");
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    await waitFor(() =>
      expect(mockCreateWorkroom).toHaveBeenCalledWith({
        name: "Epsilon Lab",
        issue_id: undefined,
      }),
    );
  });

  it("reports a create failure with an error toast and no invalidation", async () => {
    mockCreateWorkroom.mockRejectedValue(new Error("create denied"));
    renderPage();
    await screen.findByText("Alpha Sprint");

    await userEvent.type(screen.getByPlaceholderText("空间名称"), "Zeta Room");
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("创建失败"));
    expect(toast.success).not.toHaveBeenCalled();
    expect(mockListWorkrooms).toHaveBeenCalledTimes(1);
    expect(
      (screen.getByPlaceholderText("空间名称") as HTMLInputElement).value,
    ).toBe("Zeta Room");
  });
});

describe("WorkroomsPage header", () => {
  it("shows the collection header with title and description", async () => {
    renderPage();

    expect(await screen.findByText("协作空间")).toBeInTheDocument();
    expect(
      screen.getByText(
        "QM Workroom：人类与数字员工的协作上下文，绑定项目/议题/工单，不另建真源。",
      ),
    ).toBeInTheDocument();
  });

  it("shows the workroom count after loading", async () => {
    renderPage();

    await screen.findByText("Alpha Sprint");
    // CollectionPageHeader renders count as a monospace span
    const count = screen.getByText("2");
    expect(count).toBeInTheDocument();
    expect(count).toHaveClass("font-mono");
  });
});
