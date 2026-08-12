import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import {
  PipelineColumnBreakdown,
  PipelineCapabilityBar,
  __testPipelineContext,
} from "./pipeline-projection";
import type {
  ProjectPipelineCapabilityFlags,
  ProjectPipelineColumn,
  ProjectPipelineResponse,
} from "@multica/core/projects";

const healthyColumn: ProjectPipelineColumn = {
  status: "in_progress",
  total: 3,
  running: 3,
  queued: 0,
  waiting: 0,
  failed: 0,
  terminal: 0,
  terminal_no_writeback: 0,
  no_task: 0,
  unknown: 0,
};

const queuedColumn: ProjectPipelineColumn = {
  status: "in_progress",
  total: 5,
  running: 2,
  queued: 2,
  waiting: 1,
  failed: 0,
  terminal: 0,
  terminal_no_writeback: 0,
  no_task: 0,
  unknown: 0,
};

// Regression fixture: a healthy column whose ONLY non-zero counter is queued.
// Every other verbose trigger (waiting / failed / terminal_no_writeback /
// no_task / unknown) and running are zero — the exact queued-only case that
// must still expose the queued marker in the DOM.
const queuedOnlyColumn: ProjectPipelineColumn = {
  status: "todo",
  total: 4,
  running: 0,
  queued: 4,
  waiting: 0,
  failed: 0,
  terminal: 0,
  terminal_no_writeback: 0,
  no_task: 0,
  unknown: 0,
};

function wrapWithProjection(
  ui: React.ReactElement,
  data: ProjectPipelineResponse | null,
  status: "loading" | "ready" | "unavailable" = "ready",
) {
  return (
    <__testPipelineContext.Provider value={{ data, status }}>
      {ui}
    </__testPipelineContext.Provider>
  );
}

describe("PipelineColumnBreakdown", () => {
  it("shows queued count distinctly from running when queued > 0", () => {
    const { container } = renderWithI18n(<PipelineColumnBreakdown column={queuedColumn} />);
    const chip = container.querySelector("[data-pipeline-column]");
    expect(chip).toBeTruthy();
    // The queued counter has data-task-class="queued" and renders the count.
    const queuedSpan = chip!.querySelector('[data-task-class="queued"]');
    expect(queuedSpan).toBeTruthy();
    expect(queuedSpan!.textContent).toContain("2");
    // The running counter is also present — both must render independently.
    const runningSpan = chip!.querySelector(".text-blue-500");
    expect(runningSpan).toBeTruthy();
  });

  it("exposes a healthy queued-only column as verbose with a queued marker", () => {
    const { container } = renderWithI18n(
      <PipelineColumnBreakdown column={queuedOnlyColumn} />,
    );
    // The queued-only column must enter the verbose branch, so the chip is
    // annotated with the column identity and the queued count is rendered.
    const chip = container.querySelector("[data-pipeline-column]");
    expect(chip).toBeTruthy();
    expect(chip!.getAttribute("data-pipeline-column")).toBe("todo");
    const queuedSpan = chip!.querySelector('[data-task-class="queued"]');
    expect(queuedSpan).toBeTruthy();
    expect(queuedSpan!.textContent).toContain("4");
    // No running task in this fixture — the running counter must stay hidden.
    expect(chip!.querySelector(".text-blue-500")).toBeNull();
  });

  it("hides queued counter when queued is 0 on a healthy column", () => {
    renderWithI18n(<PipelineColumnBreakdown column={healthyColumn} />);
    // Healthy column with running=3 but no queued: the compact view shows
    // total + running only, no queued Clock icon.
    expect(screen.queryByLabelText("queued")).toBeNull();
  });

  it("renders total=0 for an explicitly empty column (empty state)", () => {
    const emptyCol: ProjectPipelineColumn = {
      status: "backlog",
      total: 0,
      running: 0,
      queued: 0,
      waiting: 0,
      failed: 0,
      terminal: 0,
      terminal_no_writeback: 0,
      no_task: 0,
      unknown: 0,
    };
    renderWithI18n(<PipelineColumnBreakdown column={emptyCol} />);
    // The compact/healthy view renders the total as a text node.
    expect(screen.getByText("0")).toBeTruthy();
  });
});

const allFalseFlags: ProjectPipelineCapabilityFlags = {
  cancel_task: true,
  rerun_issue: true,
  update_status: true,
  dispatch_preview: false,
  dispatch: false,
  project_start: false,
};

const allTrueFlags: ProjectPipelineCapabilityFlags = {
  cancel_task: true,
  rerun_issue: true,
  update_status: true,
  dispatch_preview: true,
  dispatch: true,
  project_start: true,
};

function makeResponse(
  flags: ProjectPipelineCapabilityFlags,
): ProjectPipelineResponse {
  return {
    project_id: "p1",
    project_status: "in_progress",
    project_title: "Test",
    updated_at: "",
    columns: {},
    issues: {},
    capability_flags: flags,
  };
}

describe("PipelineCapabilityBar", () => {
  it("renders 能力待接入 chips for unavailable capabilities", () => {
    const { container } = renderWithI18n(
      wrapWithProjection(<PipelineCapabilityBar />, makeResponse(allFalseFlags)),
    );
    const bar = container.querySelector("[data-pipeline-capability-bar]");
    expect(bar).toBeTruthy();
    expect(bar!.getAttribute("data-pending-count")).toBe("3");
    // Each pending capability has a chip with data-capability-available="false".
    const chips = bar!.querySelectorAll('[data-capability-available="false"]');
    expect(chips.length).toBe(3);
    // The 能力待接入 copy must be present.
    expect(bar!.textContent).toContain("能力待接入");
  });

  it("renders nothing when all capabilities are available", () => {
    const { container } = renderWithI18n(
      wrapWithProjection(<PipelineCapabilityBar />, makeResponse(allTrueFlags)),
    );
    expect(container.querySelector("[data-pipeline-capability-bar]")).toBeNull();
  });

  it("renders nothing when no projection data is available", () => {
    const { container } = renderWithI18n(
      wrapWithProjection(<PipelineCapabilityBar />, null, "loading"),
    );
    expect(container.querySelector("[data-pipeline-capability-bar]")).toBeNull();
  });
});
