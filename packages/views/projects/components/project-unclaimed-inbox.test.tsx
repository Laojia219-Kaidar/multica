import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ProjectUnclaimedInbox } from "./project-unclaimed-inbox";
import type { Project, WorkInboxItem } from "@multica/core/types";

// Mirror help-launcher.test.tsx: flatten the Base UI dropdown primitives so the
// menu content stays in the DOM, but preserve the ONE real invariant —
// DropdownMenuLabel wraps Base UI's Menu.GroupLabel, which throws when it has
// no Menu.Group ancestor. Rendering the label bare is exactly the VC-05 crash:
// opening the attach menu threw "Base UI: MenuGroupRootContext is missing".
vi.mock("@multica/ui/components/ui/dropdown-menu", async () => {
  const { createContext, useContext } = await import("react");
  const GroupContext = createContext(false);
  return {
    DropdownMenu: ({ children }: { children: ReactNode }) => <>{children}</>,
    DropdownMenuContent: ({ children }: { children: ReactNode }) => <>{children}</>,
    DropdownMenuItem: ({ children }: { children: ReactNode }) => <>{children}</>,
    DropdownMenuGroup: ({ children }: { children: ReactNode }) => (
      <GroupContext.Provider value={true}>{children}</GroupContext.Provider>
    ),
    DropdownMenuLabel: ({ children }: { children: ReactNode }) => {
      if (!useContext(GroupContext)) {
        throw new Error(
          "Base UI: MenuGroupRootContext is missing. Menu group parts must be used within <Menu.Group>.",
        );
      }
      return <div>{children}</div>;
    },
    DropdownMenuTrigger: ({
      render,
      children,
    }: {
      render?: ReactNode;
      children?: ReactNode;
    }) => <>{render ?? children}</>,
  };
});

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

  // VC-05: the attach menu's DropdownMenuLabel must sit inside a
  // DropdownMenuGroup. Rendering it bare made Base UI's Menu.GroupLabel throw
  // on open ("Base UI: MenuGroupRootContext is missing"), crashing the whole
  // project page when a user clicked 关联. Rendering must not throw and the
  // label + project choices must be present.
  it("renders the attach-menu label without a missing-group crash", () => {
    expect(() =>
      render(
        <ProjectUnclaimedInbox
          items={[makeInboxItem()]}
          projects={[makeProject("proj-1", "多基地经营中心")]}
          onAttach={vi.fn()}
          onIgnore={vi.fn()}
        />,
      ),
    ).not.toThrow();
    expect(screen.getByText("关联到既有项目")).toBeInTheDocument();
    expect(screen.getByText("多基地经营中心")).toBeInTheDocument();
  });
});
