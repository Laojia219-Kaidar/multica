"use client";

import { useCallback, useEffect, useState } from "react";
import {
  useDefaultLayout,
  usePanelRef,
} from "react-resizable-panels";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, FileCheck2, PanelLeftClose } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useNavigation } from "../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { ApiError } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";
import { outcomeDetailOptions } from "./outcome-queries";
import { OutcomeList } from "./outcome-list";
import { OutcomeDetail, OutcomeDetailSkeleton } from "./outcome-detail";
import { useOutcomeActions } from "./outcome-actions";
import { useOutcomesCompact } from "./use-outcomes-compact";
import { useOutcomesCursor } from "./use-outcomes-cursor";

const OUTCOME_PARAM = "outcome";
const SESSION_PARAM = "session_id";
const Q_PARAM = "q";
const STATUS_PARAM = "status";

const LIST_PANEL_ID = "list";
const DETAIL_PANEL_ID = "detail";

export function OutcomesPage() {
  const { t } = useT("outcomes");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { searchParams, replace } = useNavigation();
  const isCompact = useOutcomesCompact();

  const urlOutcome = searchParams.get(OUTCOME_PARAM) ?? "";
  const urlQ = searchParams.get(Q_PARAM) ?? "";
  const urlStatus = searchParams.get(STATUS_PARAM) ?? "";
  const urlSessionId = searchParams.get(SESSION_PARAM) ?? "";

  // The explicitly-selected review session lives in the URL, but the gate
  // needs a local mirror so the action buttons enable on the same render the
  // user picks a session (URL writes are async-routed). Mirrors the Inbox
  // selection pattern: state is the render source, the URL is the durable one.
  const [sessionId, setSessionId] = useState(urlSessionId);
  useEffect(() => {
    setSessionId(urlSessionId);
  }, [urlSessionId]);

  const writeUrl = useCallback(
    (overrides: {
      outcome?: string;
      q?: string;
      status?: string;
      sessionId?: string;
    }) => {
      const params = new URLSearchParams();
      const nextOutcome = overrides.outcome ?? urlOutcome;
      const nextQ = overrides.q ?? urlQ;
      const nextStatus = overrides.status ?? urlStatus;
      const nextSession = overrides.sessionId ?? sessionId;
      if (nextOutcome) params.set(OUTCOME_PARAM, nextOutcome);
      if (nextQ) params.set(Q_PARAM, nextQ);
      if (nextStatus) params.set(STATUS_PARAM, nextStatus);
      if (nextSession) params.set(SESSION_PARAM, nextSession);
      const query = params.toString();
      const base = wsPaths.outcomes();
      replace(query ? `${base}?${query}` : base);
    },
    [urlOutcome, urlQ, urlStatus, sessionId, wsPaths, replace],
  );

  // Debounced search commits to the URL (and therefore the real API query).
  const [qDraft, setQDraft] = useState(urlQ);
  useEffect(() => {
    setQDraft(urlQ);
  }, [urlQ]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (qDraft === urlQ) return;
      writeUrl({ q: qDraft });
    }, 250);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [qDraft]);

  const cursorList = useOutcomesCursor(wsId, {
    q: urlQ || undefined,
    status: urlStatus || undefined,
    limit: 50,
    offset: 0,
  });

  const detailQuery = useQuery(
    outcomeDetailOptions(wsId, urlOutcome),
  );

  const actions = useOutcomeActions(wsId, urlOutcome);

  // Re-read: refetch the detail so the owner sees the latest authority-backed
  // state after an external readback. State-driven so the re-read button's busy
  // spinner renders.
  const [rereading, setRereading] = useState(false);
  const reread = useCallback(() => {
    setRereading(true);
    void detailQuery.refetch().finally(() => {
      setRereading(false);
    });
  }, [detailQuery]);

  const outcomes = cursorList.outcomes;

  // Invalid/deleted outcome: an explicit URL selection that does not resolve
  // must surface a not-found state, never silently fall back to the first row.
  const detailNotFound =
    !!urlOutcome &&
    detailQuery.isError &&
    detailQuery.error instanceof ApiError &&
    detailQuery.error.status === 404;

  const handleSelect = useCallback(
    (commandId: string) => {
      writeUrl({ outcome: commandId });
    },
    [writeUrl],
  );

  const handleSessionIdChange = useCallback(
    (nextSessionId: string) => {
      setSessionId(nextSessionId);
      writeUrl({ sessionId: nextSessionId });
    },
    [writeUrl],
  );

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_outcomes_layout",
  });
  const listPanelRef = usePanelRef();

  const toggleListPanel = useCallback(() => {
    const panel = listPanelRef.current;
    if (!panel) return;
    if (panel.isCollapsed()) panel.expand();
    else panel.collapse();
  }, [listPanelRef]);

  const listHeader = (
    <PageHeader className="gap-2">
      <FileCheck2 className="h-4 w-4 text-muted-foreground" />
      <h1 className="text-sm font-semibold">{t(($) => $.page.title)}</h1>
      <Button
        variant="ghost"
        size="icon-sm"
        className="ml-auto text-muted-foreground"
        onClick={toggleListPanel}
        title={t(($) => $.list.collapse)}
        aria-label={t(($) => $.list.collapse)}
      >
        <PanelLeftClose className="h-4 w-4" />
      </Button>
    </PageHeader>
  );

  const listBody = (
    <OutcomeList
      outcomes={outcomes}
      total={cursorList.total}
      loading={cursorList.loading}
      loadingMore={cursorList.loadingMore}
      hasMore={cursorList.hasMore}
      error={cursorList.error}
      selectedCommandId={urlOutcome}
      q={qDraft}
      status={urlStatus}
      onQChange={setQDraft}
      onStatusChange={(status) => writeUrl({ status })}
      onSelect={(o) => handleSelect(o.id)}
      onLoadMore={cursorList.loadMore}
    />
  );

  const detailContent = detailNotFound ? (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <FileCheck2 className="h-10 w-10 text-muted-foreground/30" />
      <p className="text-base font-medium text-foreground">
        {t(($) => $.page.not_found_title)}
      </p>
      <p className="text-sm">{t(($) => $.page.not_found_description)}</p>
    </div>
  ) : detailQuery.isPending && urlOutcome ? (
    <OutcomeDetailSkeleton />
  ) : detailQuery.error && urlOutcome ? (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <FileCheck2 className="h-10 w-10 text-muted-foreground/30" />
      <p className="text-sm">{t(($) => $.page.error_description)}</p>
    </div>
  ) : detailQuery.data && urlOutcome ? (
    <OutcomeDetail
      wsId={wsId}
      detail={detailQuery.data}
      sessionId={sessionId}
      onSessionIdChange={handleSessionIdChange}
      actions={actions}
      onReread={reread}
      rereading={rereading}
    />
  ) : (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <FileCheck2 className="h-10 w-10 text-muted-foreground/30" />
      <p className="text-sm">{t(($) => $.page.select_prompt)}</p>
    </div>
  );

  // -- Mobile (≤720px): single column list/detail toggle ---------------------
  if (isCompact) {
    if (urlOutcome) {
      return (
        <div className="flex flex-1 flex-col min-h-0">
          <div className="flex h-12 shrink-0 items-center border-b px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => writeUrl({ outcome: "" })}
              className="gap-1.5 text-muted-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {t(($) => $.detail.back)}
            </Button>
          </div>
          <div className="flex-1 min-h-0 overflow-hidden">{detailContent}</div>
        </div>
      );
    }
    return (
      <div className="flex flex-1 flex-col min-h-0">
        {listHeader}
        <div className="flex-1 min-h-0 overflow-hidden">{listBody}</div>
      </div>
    );
  }

  // -- Desktop: resizable two-panel -------------------------------------------
  return (
    <ResizablePanelGroup
      orientation="horizontal"
      className="flex-1 min-h-0"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id={LIST_PANEL_ID}
        defaultSize={320}
        minSize={240}
        maxSize={480}
        collapsible
        collapsedSize={0}
        groupResizeBehavior="preserve-pixel-size"
        panelRef={listPanelRef}
      >
        <div className="flex flex-col border-r h-full">
          {listHeader}
          <div className="flex-1 min-h-0 overflow-hidden">{listBody}</div>
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id={DETAIL_PANEL_ID} minSize="40%">
        <div className="flex flex-col min-h-0 h-full">{detailContent}</div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}