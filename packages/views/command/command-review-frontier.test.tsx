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

  it("keeps an Authority evidence gap visibly blocked while preserving read-only detail", () => {
    const item = makeItem({ reviewStateReason: "authority_evidence_missing" });
    render(<CommandReviewFrontier {...commonProps} issues={[item]} />);

    expect(reviewQueueDisposition(item)).toBe("blocked");
    expect(screen.getByText("authority_evidence_missing")).toBeInTheDocument();
    expect(screen.getByText("Blocked")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open read-only detail/ })).toHaveAttribute(
      "href",
      `/hivecosm/issues/${item.issueId}`,
    );
  });

  it("makes an Owner decision and the canonical Outcome Center discoverable", () => {
    const item = makeItem({
      reviewState: "owner_decision",
      reviewerAgentId: null,
      reviewTargetTaskId: null,
    });
    render(<CommandReviewFrontier {...commonProps} issues={[item]} />);

    expect(reviewQueueDisposition(item)).toBe("owner_decision");
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
});
