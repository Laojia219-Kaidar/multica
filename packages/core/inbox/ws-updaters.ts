import type { QueryClient } from "@tanstack/react-query";
import { inboxKeys } from "./queries";
import type { InboxItem, IssueStatus } from "../types";

interface InboxListEnvelope {
  items: InboxItem[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
}

type InboxListCache = InboxItem[] | InboxListEnvelope;

function extractItems(old: unknown): InboxItem[] | undefined {
  if (Array.isArray(old)) return old;
  if (
    old != null &&
    typeof old === "object" &&
    Array.isArray((old as InboxListEnvelope).items)
  ) {
    return (old as InboxListEnvelope).items;
  }
  return undefined;
}

function writeItems(
  original: InboxListCache | undefined,
  next: InboxItem[],
): InboxListCache {
  if (
    original != null &&
    !Array.isArray(original) &&
    typeof original === "object" &&
    Array.isArray((original as InboxListEnvelope).items)
  ) {
    return { ...(original as InboxListEnvelope), items: next };
  }
  return next;
}

function patchCache(
  qc: QueryClient,
  key: readonly string[],
  wsId: string,
  transform: (items: InboxItem[]) => InboxItem[],
) {
  const fullKey = [...key];
  const old = qc.getQueryData(fullKey);
  if (old === undefined) return;
  const items = extractItems(old);
  if (!items) {
    qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
    return;
  }
  qc.setQueryData<InboxListCache>(fullKey, () =>
    writeItems(old as InboxListCache, transform(items)),
  );
}

export function onInboxNew(
  qc: QueryClient,
  wsId: string,
  _item: InboxItem,
) {
  // Use invalidateQueries instead of setQueryData — triggers a refetch that
  // reliably notifies all observers. The inbox list is small so this is cheap.
  //
  // Both lists: a new notification on an ARCHIVED issue puts that issue back in
  // the main inbox, which means it must also leave the archived list. The
  // server owns that split (ListArchivedInboxItems excludes issues with an
  // active row), so refetching both is what keeps them mutually exclusive.
  qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
}

export function patchInboxIssueStatus(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  status: IssueStatus,
) {
  const transform = (items: InboxItem[]) =>
    items.map((i) =>
      i.issue_id === issueId ? { ...i, issue_status: status } : i,
    );
  patchCache(qc, inboxKeys.list(wsId), wsId, transform);
  // Archived rows render the same status icon, so they need the same patch.
  patchCache(qc, inboxKeys.archived(wsId), wsId, transform);
}

export function onInboxIssueStatusChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  status: IssueStatus,
) {
  patchInboxIssueStatus(qc, wsId, issueId, status);
}

// Mirrors the DB-level ON DELETE CASCADE on inbox_item.issue_id: when an issue
// is deleted, all inbox items that referenced it are gone server-side, so drop
// them from the cache too — from the archived list as well, which holds rows
// for the same issues.
export function onInboxIssueDeleted(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  const transform = (items: InboxItem[]) =>
    items.filter((i) => i.issue_id !== issueId);
  patchCache(qc, inboxKeys.list(wsId), wsId, transform);
  patchCache(qc, inboxKeys.archived(wsId), wsId, transform);
}

// Refresh both the main and archived lists. Every inbox event can move an item
// across that boundary (archive, unarchive, or a new notification reviving an
// archived issue), and the split is decided server-side, so the two are always
// invalidated together.
export function onInboxInvalidate(qc: QueryClient, wsId: string) {
  qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
}

// Refresh the cross-workspace unread summary (workspace-switcher dot). The
// summary spans every workspace, so it is invalidated on ANY inbox event
// regardless of which workspace the event came from — including read/archive
// events from a workspace other than the active one, which the workspace-
// scoped list invalidation cannot reach.
export function onInboxSummaryInvalidate(qc: QueryClient) {
  qc.invalidateQueries({ queryKey: inboxKeys.unreadSummary() });
}
