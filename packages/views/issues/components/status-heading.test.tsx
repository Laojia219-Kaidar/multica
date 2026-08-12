import { describe, expect, it } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { StatusHeading } from "./status-heading";
import { __testPipelineContext } from "../../projects/components/pipeline-projection";
import type {
  ProjectPipelineColumn,
  ProjectPipelineResponse,
} from "@multica/core/projects";

function wrapProjection(
  ui: React.ReactElement,
  data: ProjectPipelineResponse | null,
  status: "loading" | "ready" | "unavailable",
) {
  return (
    <__testPipelineContext.Provider value={{ data, status }}>
      {ui}
    </__testPipelineContext.Provider>
  );
}

const emptyColumn: ProjectPipelineColumn = {
  status: "todo",
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

const responseWithColumn = (
  col: ProjectPipelineColumn,
): ProjectPipelineResponse => ({
  project_id: "p1",
  project_status: "in_progress",
  project_title: "Test",
  updated_at: "",
  columns: { [col.status]: col },
  issues: {},
  capability_flags: {
    cancel_task: true,
    rerun_issue: true,
    update_status: true,
    dispatch_preview: false,
    dispatch: false,
    project_start: false,
  },
});

describe("StatusHeading projection lifecycle (§8)", () => {
  it("shows a loading indicator when projection is loading (not plain count)", () => {
    const { container } = renderWithI18n(
      wrapProjection(<StatusHeading status="todo" count={5} />, null, "loading"),
    );
    // The loading node has data-pipeline-loading attribute.
    expect(container.querySelector("[data-pipeline-loading]")).toBeTruthy();
    // The plain count must NOT be shown during loading — that would be a
    // silent fallback the contract forbids.
    expect(container.textContent).not.toContain("5");
  });

  it("shows an unavailable marker when projection errored (not silent fallback)", () => {
    const { container } = renderWithI18n(
      wrapProjection(
        <StatusHeading status="todo" count={5} />,
        null,
        "unavailable",
      ),
    );
    // The unavailable node has data-pipeline-unavailable.
    expect(container.querySelector("[data-pipeline-unavailable]")).toBeTruthy();
    // The plain count is still shown alongside the marker (for graceful
    // degradation), but the marker makes it explicit that the projection
    // is stale.
    expect(container.textContent).toContain("5");
  });

  it("shows the column breakdown (total=0) when projection is ready and column is empty", () => {
    const { container } = renderWithI18n(
      wrapProjection(
        <StatusHeading status="todo" count={0} />,
        responseWithColumn(emptyColumn),
        "ready",
      ),
    );
    // Ready + empty: PipelineColumnBreakdown renders total=0 explicitly.
    expect(container.textContent).toContain("0");
    // No loading or unavailable markers.
    expect(container.querySelector("[data-pipeline-loading]")).toBeNull();
    expect(container.querySelector("[data-pipeline-unavailable]")).toBeNull();
  });

  it("shows the plain count on a non-project surface (no provider)", () => {
    // Without a provider, usePipelineProjectionStatus returns "none" and the
    // heading preserves its original plain-count look.
    const { container } = renderWithI18n(<StatusHeading status="todo" count={7} />);
    expect(container.textContent).toContain("7");
    expect(container.querySelector("[data-pipeline-loading]")).toBeNull();
    expect(container.querySelector("[data-pipeline-unavailable]")).toBeNull();
  });
});
