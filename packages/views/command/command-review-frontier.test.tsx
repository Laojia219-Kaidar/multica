import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ReviewQueueItem } from "@multica/core/types";
import {
  CommandReviewFrontier,
  reviewQueueDisposition,
  type CommandReviewFrontierCopy,
} from "./command-review-frontier";

vi.mock("../navigation", () => ({
  AppLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

const copy: CommandReviewFrontierCopy = {
  eyebrow: "Review frontier",
  title: "Owner review queue",
  description: "Authority-backed work only.",
  loading: "Loading review queue",
  empty: "No open review items",
  blockedTitle: "Review queue unavailable",
  blockedDescription: "No review action has been enabled.",
  ownerReady: "Owner decision",
  activeReview: "Review active",
  blockedReview: "Blocked",
  openOwnerDecision: "Open Owner decision",
  openDetail: "Open read-only detail",
  reviewer: "Reviewer",
  outcomeCenter: "Open Outcome Center",
};

function makeItem(overrides: Partial<ReviewQueueItem> = {}): ReviewQueueItem {
  return {
    issueId: "00000000-0000-4000-8000-000000000001",
    identifier: "HIV-721",
    title: "Owner-to-Outcome review",
    reviewState: "evidence_review",
    reviewStateReason: null,
    reviewerAgentId: "00000000-0000-4000-8000-000000000002",
    reviewerName: "Gauss",
    reviewTargetTaskId: "00000000-0000-4000-8000-000000000003",
    reviewTaskStatus: "running",
    issueUpdatedAt: "2026-08-20T12:00:00Z",
    ...overrides,
  };
}

const commonProps = {
  loading: false,
  error: false,
  authorityReady: true,
  outcomeCenterReady: true,
  issueHref: (id: string) => `/hivecosm/issues/${id}`,
  outcomesHref: "/hivecosm/outcomes",
  copy,
};

describe("CommandReviewFrontier", () => {
  it("shows an explicit blocked state when the read model is unavailable", () => {
    render(<CommandReviewFrontier {...commonProps} issues={[]} error />);

    expect(screen.getByTestId("review-frontier-blocked")).toHaveTextContent("Review queue unavailable");
    expect(screen.queryByText("No open review items")).not.toBeInTheDocument();
    expect(screen.queryByText("Open Owner decision")).not.toBeInTheDocument();
  });

  it("keeps a missing Authority provider visibly blocked while preserving read-only detail", () => {
    const item = makeItem();
    render(<CommandReviewFrontier {...commonProps} issues={[item]} authorityReady={false} />);

    expect(reviewQueueDisposition(item, { authorityReady: false, outcomeCenterReady: true })).toBe("blocked");
    expect(screen.getByTestId("review-frontier-blocked")).toBeInTheDocument();
    expect(screen.getByText("Blocked")).toBeInTheDocument();
    expect(screen.queryByText("Review active")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open read-only detail/ })).toHaveAttribute(
      "href",
      `/hivecosm/issues/${item.issueId}`,
    );
  });

  it("shows an active review only for a provider-backed active task", () => {
    const item = makeItem();
    render(<CommandReviewFrontier {...commonProps} issues={[item]} />);

    expect(reviewQueueDisposition(item, { authorityReady: true, outcomeCenterReady: true })).toBe("active");
    expect(screen.getByText("Review active")).toBeInTheDocument();
  });

  it("blocks a stale review row when its active task status is absent", () => {
    const item = makeItem({ reviewTaskStatus: null });
    render(<CommandReviewFrontier {...commonProps} issues={[item]} />);

    expect(reviewQueueDisposition(item, { authorityReady: true, outcomeCenterReady: true })).toBe("blocked");
    expect(screen.getByText("Blocked")).toBeInTheDocument();
    expect(screen.queryByText("Review active")).not.toBeInTheDocument();
  });

  it("makes an Owner decision and the canonical Outcome Center discoverable", () => {
    const item = makeItem({
      reviewState: "owner_decision",
      reviewStateReason: "no_source_task_id",
      reviewerAgentId: null,
      reviewTargetTaskId: null,
    });
    render(<CommandReviewFrontier {...commonProps} issues={[item]} />);

    expect(reviewQueueDisposition(item, { authorityReady: true, outcomeCenterReady: true })).toBe("owner_decision");
    expect(screen.getByText("no_source_task_id")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open Owner decision/ })).toHaveAttribute(
      "href",
      `/hivecosm/issues/${item.issueId}`,
    );
    expect(screen.getByRole("link", { name: /Open Outcome Center/ })).toHaveAttribute(
      "href",
      "/hivecosm/outcomes",
    );
  });

  it("distinguishes a successful empty queue from a failed read", () => {
    render(<CommandReviewFrontier {...commonProps} issues={[]} />);

    expect(screen.getByText("No open review items")).toBeInTheDocument();
    expect(screen.queryByTestId("review-frontier-blocked")).not.toBeInTheDocument();
  });

  it("does not claim an empty queue or link to outcomes when the Outcome read model is absent", () => {
    render(
      <CommandReviewFrontier
        {...commonProps}
        issues={[]}
        outcomeCenterReady={false}
      />,
    );

    expect(screen.getByTestId("review-frontier-blocked")).toBeInTheDocument();
    expect(screen.queryByText("No open review items")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Open Outcome Center/ })).not.toBeInTheDocument();
    expect(screen.getByText("Open Outcome Center")).toHaveAttribute("aria-disabled", "true");
  });
});
