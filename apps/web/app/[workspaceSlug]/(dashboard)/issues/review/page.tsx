"use client";

import { useCallback, useEffect, useState } from "react";
import { getCurrentSlug } from "@multica/core/platform";
import Link from "next/link";

type ReviewQueueItem = {
  issue_id: string;
  identifier: string;
  title: string;
  review_state: string | null;
  review_state_reason?: string | null;
  reviewer_agent_id?: string | null;
  reviewer_name?: string | null;
  review_target_task_id?: string | null;
  review_task_status?: string | null;
  issue_updated_at: string;
};

type VerdictRequest = {
  verdict: "pass" | "revise";
  notes: string;
  repair_requirements?: string[];
};

// Lane B / P2 review queue + verdict surface. This is a self-contained view:
// it reads GET /api/issues/review-queue and posts PASS/REVISE verdicts to
// POST /api/issues/{id}/review-verdict. Member owners may PASS or REVISE;
// assigned reviewer agents reach the same endpoints through the daemon/CLI.
export default function ReviewQueuePage() {
  const slug = getCurrentSlug();
  const [items, setItems] = useState<ReviewQueueItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const res = await fetch("/api/issues/review-queue", {
        credentials: "include",
        headers: {
          "Content-Type": "application/json",
          "X-Workspace-Slug": slug ?? "",
        },
      });
      if (!res.ok) throw new Error(`review-queue ${res.status}`);
      const body = await res.json();
      setItems((body?.issues ?? []) as ReviewQueueItem[]);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [slug]);

  useEffect(() => {
    reload();
  }, [reload]);

  const submitVerdict = useCallback(
    async (issueId: string, verdict: "pass" | "revise", notes: string) => {
      setBusy(issueId);
      try {
        const payload: VerdictRequest = { verdict, notes };
        const res = await fetch(`/api/issues/${issueId}/review-verdict`, {
          method: "POST",
          credentials: "include",
          headers: {
            "Content-Type": "application/json",
            "X-Workspace-Slug": slug ?? "",
          },
          body: JSON.stringify(payload),
        });
        if (!res.ok) {
          const text = await res.text();
          throw new Error(`verdict ${res.status}: ${text}`);
        }
        await reload();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(null);
      }
    },
    [reload, slug],
  );

  return (
    <div className="mx-auto max-w-4xl px-4 py-6">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-lg font-semibold">Review queue</h1>
        <button type="button" onClick={reload} className="rounded-md border px-3 py-1 text-sm">
          Refresh
        </button>
      </div>
      {error && <p className="mb-3 rounded-md bg-rose-50 px-3 py-2 text-sm text-rose-700">{error}</p>}
      {items.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">No open review items in this workspace.</p>
      )}
      <ul className="space-y-3">
        {items.map((item) => (
          <li key={item.issue_id} className="rounded-lg border p-3">
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <Link href={`/${slug}/issues/${item.issue_id}`} className="text-sm font-medium hover:underline">
                  {item.identifier} · {item.title}
                </Link>
                <p className="mt-1 text-xs text-muted-foreground">
                  state: {item.review_state ?? "—"}
                  {item.reviewer_name ? ` · reviewer: ${item.reviewer_name}` : ""}
                  {item.review_state_reason ? ` · ${item.review_state_reason}` : ""}
                </p>
              </div>
              <div className="flex shrink-0 gap-2">
                <button
                  type="button"
                  disabled={busy === item.issue_id}
                  onClick={() => submitVerdict(item.issue_id, "revise", "")}
                  className="rounded-md border border-amber-300 px-3 py-1 text-xs text-amber-700 hover:bg-amber-50"
                >
                  REVISE
                </button>
                <button
                  type="button"
                  disabled={busy === item.issue_id}
                  onClick={() => submitVerdict(item.issue_id, "pass", "Accepted from review queue")}
                  className="rounded-md border border-emerald-300 px-3 py-1 text-xs text-emerald-700 hover:bg-emerald-50"
                >
                  PASS
                </button>
              </div>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
