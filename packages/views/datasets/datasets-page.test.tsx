// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { renderWithI18n } from "../test/i18n";
import { DatasetsPage } from "./datasets-page";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const agentUuid = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";

const dataset = () => ({
  id: "11111111-2222-4333-8444-555555555555",
  name: "产品文档集",
  domain: "项目成果",
  product_type: "rag_kb",
  version: 2,
  authorized_agent_ids: [agentUuid],
});

const employees = () => [
  { id: "emp-1", name: "Coco", agent_id: agentUuid, status: "active" },
  { id: "emp-2", name: "Turing", status: "active" },
];

const mockListDatasets = vi.hoisted(() => vi.fn());
const mockListEmployees = vi.hoisted(() => vi.fn());
const mockUpdateDataset = vi.hoisted(() => vi.fn());
const mockDeleteDataset = vi.hoisted(() => vi.fn());

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <DatasetsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockListDatasets.mockResolvedValue([dataset()]);
  mockListEmployees.mockResolvedValue(employees());
  mockUpdateDataset.mockResolvedValue({ ...dataset() });
  mockDeleteDataset.mockResolvedValue(undefined);
  setApiInstance({
    listDatasets: mockListDatasets,
    listEmployees: mockListEmployees,
    updateDataset: mockUpdateDataset,
    deleteDataset: mockDeleteDataset,
  } as unknown as ApiClient);
});

describe("DatasetsPage", () => {
  it("shows the honest World Library empty state when no datasets exist", async () => {
    mockListDatasets.mockResolvedValue([]);
    renderPage();
    expect(await screen.findByText(/暂无数据集/)).toBeInTheDocument();
    // The authority declaration appears in the header and the empty state.
    expect(screen.getAllByText(/source_available_runtime_unavailable/).length).toBeGreaterThanOrEqual(2);
  });

  it("projects employee authorization through bound agents", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("产品文档集")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /授权员工/ }));

    const coco = await screen.findByRole("checkbox", { name: "授权员工 Coco" });
    expect(coco).toBeChecked();
    // Turing has no bound Agent, so the employee stays visible but disabled
    // with an explicit hint — hiding them would not be an honest projection.
    const turing = screen.getByRole("checkbox", { name: "授权员工 Turing" });
    expect(turing).toBeDisabled();
    expect(screen.getByText("未绑定 Agent")).toBeInTheDocument();
  });

  it("authorizes an employee by writing the bound agent id", async () => {
    const user = userEvent.setup();
    mockListDatasets.mockResolvedValue([{ ...dataset(), authorized_agent_ids: [] }]);
    renderPage();

    expect(await screen.findByText("产品文档集")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /授权员工/ }));
    const coco = await screen.findByRole("checkbox", { name: "授权员工 Coco" });
    expect(coco).not.toBeChecked();

    await user.click(coco);
    await waitFor(() => {
      expect(mockUpdateDataset).toHaveBeenCalledWith(dataset().id, {
        authorized_agent_ids: [agentUuid],
      });
    });
  });

  it("renames a dataset through the inline editor", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("产品文档集")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重命名" }));
    const input = screen.getByLabelText("数据集名称");
    await user.clear(input);
    await user.type(input, "产品手册集");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => {
      expect(mockUpdateDataset).toHaveBeenCalledWith(dataset().id, { name: "产品手册集" });
    });
  });

  it("bumps the dataset version", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("产品文档集")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "升版 v3" }));
    await waitFor(() => {
      expect(mockUpdateDataset).toHaveBeenCalledWith(dataset().id, { version: 3 });
    });
  });

  it("deletes a dataset after confirmation", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("产品文档集")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "删除" }));
    await waitFor(() => {
      expect(mockDeleteDataset).toHaveBeenCalledWith(dataset().id);
    });
    expect(confirmSpy).toHaveBeenCalled();
  });

  it("does not delete when the confirmation is declined", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("产品文档集")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "删除" }));
    expect(mockDeleteDataset).not.toHaveBeenCalled();
  });
});
