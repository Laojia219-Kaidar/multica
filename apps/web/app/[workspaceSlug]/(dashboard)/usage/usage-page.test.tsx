import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/paths", () => ({
  useRequiredWorkspaceSlug: () => "hivecosm",
}));
vi.mock("@multica/core/api", () => ({
  getApi: () => ({ getBaseUrl: () => "" }),
}));

const { mockFetchUsageHierarchy } = vi.hoisted(() => ({
  mockFetchUsageHierarchy: vi.fn(),
}));

vi.mock("./usage-api", () => ({
  fetchUsageHierarchy: (...args: unknown[]) => mockFetchUsageHierarchy(...args),
}));

import { UsagePage } from "./usage-page";
import type { UsageHierarchy } from "./usage-api";

const fixture: UsageHierarchy = {
  workspace_id: "ws-1",
  since: "2026-08-13T00:00:00Z",
  generated_at: "2026-08-13T14:00:00Z",
  data_gaps: ["quota_unconfigured"],
  totals: {
    used_tokens: 4_500_000_000,
    task_count: 619,
    employee_count: 32,
    plan_count: 28,
    local_model_count: 1,
  },
  providers: [
    {
      provider: "阿里云 · Qwen",
      local_model: false,
      used_tokens: 313_777_232,
      plans: [
        {
          plan: "Qwen Coding Plan",
          account: "HiveCosm Secure qwen-coding (HiveCosm Mac mini)",
          api_key_label: "qwen-coding-1",
          local_model: false,
          used_tokens: 313_777_232,
          quota: {
            cycle: "monthly",
            total_tokens: 1_000_000_000,
            used_tokens: 313_777_232,
            remaining_tokens: 686_222_768,
            percentage: 31.3777232,
            reset_at: "2026-09-01T00:00:00Z",
            reset_day: 1,
            local_model: false,
          },
          models: [
            { model: "qwen3.7-plus", used_tokens: 313_777_232, employee_count: 3, task_count: 10 },
          ],
          employees: [
            {
              agent_id: "a1",
              name: "Coco｜首席运营官",
              used_tokens: 313_777_232,
              models: [],
              tasks: [
                { task_id: "t1", model: "qwen3.7-plus", used_tokens: 1000, cost_usd_ticks: 0 },
              ],
            },
          ],
        },
        {
          plan: "Qwen Code",
          account: "Qwen Code (HiveCosm Mac mini)",
          local_model: false,
          used_tokens: 53_881_947,
          quota: null,
          models: [],
          employees: [],
        },
      ],
    },
    {
      provider: "本地模型 · DGX",
      local_model: true,
      used_tokens: 600,
      plans: [
        {
          plan: "qwen3.6-27B NVFP4",
          account: "dgx-local",
          local_model: true,
          used_tokens: 600,
          quota: null,
          models: [],
          employees: [],
        },
      ],
    },
  ],
};

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <UsagePage />
    </QueryClientProvider>,
  );
}

describe("UsagePage — hierarchy aggregation render", () => {
  it("renders provider, plan quota fields, local-model badge and data-gap banner", async () => {
    mockFetchUsageHierarchy.mockResolvedValue(fixture);

    renderPage();

    expect(await screen.findByText("阿里云 · Qwen")).toBeInTheDocument();
    expect(await screen.findByText("Qwen Coding Plan")).toBeInTheDocument();
    // Quota percentage rendered from real percentage field.
    expect(await screen.findByText("31.4%")).toBeInTheDocument();
    // Remaining quota rendered.
    expect(screen.getByText(/剩余 686\.22M/)).toBeInTheDocument();
    // API-key identifier (non-secret label) rendered.
    expect(screen.getByText(/API key · qwen-coding-1/)).toBeInTheDocument();
    // Unconfigured quota honesty.
    expect(screen.getAllByText("配额未配置").length).toBeGreaterThan(0);
    // Local model provider flagged.
    expect(screen.getAllByText("本地模型").length).toBeGreaterThan(0);
    // Data-gap banner renders the honest gap, not a fabricated number.
    expect(screen.getByTestId("data-gap-banner")).toBeInTheDocument();
    // Totals.
    expect(screen.getByText("4.50B")).toBeInTheDocument();
  });
});
