import { cleanup, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const memberState = vi.hoisted(() => ({
  role: null as "owner" | "admin" | "member" | null,
  isLoading: false,
  isError: false,
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({
    userId: memberState.role ? "user-1" : null,
    role: memberState.role,
    member: null,
    isLoading: memberState.isLoading,
    isError: memberState.isError,
  }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    previewIssueDispatch: vi.fn(),
    dispatchIssue: vi.fn(),
    stopIssue: vi.fn(),
    sendIssueToReview: vi.fn(),
  },
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueKeys: {
    detail: () => ["issue-detail"],
    list: () => ["issue-list"],
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({ children, ...props }: any) => <button {...props}>{children}</button>,
}));

import { IssueDispatchControls } from "./issue-dispatch-controls";

beforeEach(() => {
  cleanup();
  memberState.role = null;
  memberState.isLoading = false;
  memberState.isError = false;
});

describe("IssueDispatchControls membership gate", () => {
  const props = { issueId: "issue-1", issueStatus: "todo", workspaceId: "ws-1" };

  it.each(["owner", "admin"] as const)("renders controls for %s", (role) => {
    memberState.role = role;
    render(<IssueDispatchControls {...props} />);
    expect(screen.getByRole("button", { name: "派工预览" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "停止" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "送审" })).toBeTruthy();
  });

  it.each([
    ["member", "member", false, false],
    ["no member", null, false, false],
    ["loading", "owner", true, false],
    ["error with cached owner", "owner", false, true],
  ] as const)("hides controls for %s", (_name, role, isLoading, isError) => {
    memberState.role = role;
    memberState.isLoading = isLoading;
    memberState.isError = isError;
    render(<IssueDispatchControls {...props} />);
    expect(screen.queryByRole("button", { name: "派工预览" })).toBeNull();
    expect(screen.queryByRole("button", { name: "停止" })).toBeNull();
    expect(screen.queryByRole("button", { name: "送审" })).toBeNull();
  });
});
