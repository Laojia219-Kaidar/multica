import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("@multica/core/platform", () => ({
  getCurrentSlug: () => "hivecosm",
}));

vi.mock("next/link", () => ({
  default: ({ href, children }: { href: string; children: React.ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

import ReviewQueuePage from "./page";

describe("ReviewQueuePage", () => {
  it("binds queue reads and verdict writes to the active workspace", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          issues: [
            {
              issue_id: "issue-1",
              identifier: "HIV-719",
              title: "Canary repair",
              review_state: "review_requested",
              issue_updated_at: "2026-08-19T00:00:00Z",
            },
          ],
        }),
      })
      .mockResolvedValueOnce({ ok: true })
      .mockResolvedValueOnce({ ok: true, json: async () => ({ issues: [] }) });
    vi.stubGlobal("fetch", fetchMock);

    render(<ReviewQueuePage />);

    expect(await screen.findByText(/HIV-719/)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/api/issues/review-queue",
      expect.objectContaining({
        headers: expect.objectContaining({ "X-Workspace-Slug": "hivecosm" }),
      }),
    );

    await userEvent.click(screen.getByRole("button", { name: "PASS" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/issues/issue-1/review-verdict",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-Workspace-Slug": "hivecosm" }),
      }),
    );
  });
});
