import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ProjectUnclaimedInbox } from "./project-unclaimed-inbox";
import type { Project, WorkInboxItem } from "@multica/core/types";

function makeInboxItem(overrides: Partial<WorkInboxItem> = {}): WorkInboxItem {
  return {
    ID: "inbox-1",
    WorkspaceID: "ws-1",
    WorkRef: "hivecrew://ws-1/work/inbox/abc123",
    ...overrides,
  };
}

function makeProject(id: string, title: string): Project {
  return {
    id,
    title,
    workspace_id: "ws-1",
    status: "in_progress",
    priority: "medium",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    lead_type: null,
    lead_id: null,
    icon: null,
    description: null,
    start_date: null,
    due_date: null,
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
  };
}

describe("ProjectUnclaimedInbox", () => {
  it("renders the empty state", () => {
    render(
      <ProjectUnclaimedInbox
        items={[]}
        projects={[]}
        onAttach={vi.fn()}
        onIgnore={vi.fn()}
      />,
    );
    expect(screen.getByText("没有未登记的工作动作。")).toBeInTheDocument();
  });

  it("lists work refs and states the no-progress rule", () => {
    render(
      <ProjectUnclaimedInbox
        items={[
          makeInboxItem(),
          makeInboxItem({ ID: "inbox-2", WorkRef: "hivecrew://ws-1/work/inbox/def456" }),
        ]}
        projects={[makeProject("proj-1", "多基地经营中心")]}
        onAttach={vi.fn()}
        onIgnore={vi.fn()}
      />,
    );
    expect(
      screen.getByText("hivecrew://ws-1/work/inbox/abc123"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("未归属动作不推进任何项目进度"),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "关联" })).toHaveLength(2);
  });

  it("fires onIgnore for the selected item", () => {
    const onIgnore = vi.fn();
    render(
      <ProjectUnclaimedInbox
        items={[makeInboxItem()]}
        projects={[]}
        onAttach={vi.fn()}
        onIgnore={onIgnore}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "忽略" }));
    expect(onIgnore).toHaveBeenCalledWith(
      expect.objectContaining({ ID: "inbox-1" }),
    );
  });
});
