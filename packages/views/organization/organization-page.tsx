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
import { ArrowLeft, Building2, PanelLeftClose, UserRound } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useNavigation } from "../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { ApiError } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";
import {
  employeeDossierOptions,
  organizationTreeOptions,
  rosterListOptions,
} from "./organization-queries";
import { OrgTree } from "./org-tree";
import { RosterList } from "./roster-list";
import {
  EmployeeDossier,
  EmployeeDossierSkeleton,
} from "./employee-dossier";
import { SourceGapBanner } from "./source-gap";
import { useOrganizationCompact } from "./use-organization-compact";

const TAB_PARAM = "tab";
const EMPLOYEE_PARAM = "employee";
const Q_PARAM = "q";
const STATUS_PARAM = "status";

const LIST_PANEL_ID = "list";
const DETAIL_PANEL_ID = "detail";

export function OrganizationPage() {
  const { t } = useT("organization");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { searchParams, replace } = useNavigation();
  const isCompact = useOrganizationCompact();

  const urlTab = searchParams.get(TAB_PARAM) ?? "";
  const urlEmployee = searchParams.get(EMPLOYEE_PARAM) ?? "";
  const urlQ = searchParams.get(Q_PARAM) ?? "";
  const urlStatus = searchParams.get(STATUS_PARAM) ?? "";

  const writeUrl = useCallback(
    (overrides: {
      tab?: string;
      employee?: string;
      q?: string;
      status?: string;
    }) => {
      const params = new URLSearchParams();
      const nextTab = overrides.tab ?? urlTab;
      const nextEmployee = overrides.employee ?? urlEmployee;
      const nextQ = overrides.q ?? urlQ;
      const nextStatus = overrides.status ?? urlStatus;
      if (nextTab) params.set(TAB_PARAM, nextTab);
      if (nextEmployee) params.set(EMPLOYEE_PARAM, nextEmployee);
      if (nextQ) params.set(Q_PARAM, nextQ);
      if (nextStatus) params.set(STATUS_PARAM, nextStatus);
      const query = params.toString();
      const base = wsPaths.organization();
      replace(query ? `${base}?${query}` : base);
    },
    [urlTab, urlEmployee, urlQ, urlStatus, wsPaths, replace],
  );

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

  const treeQuery = useQuery(organizationTreeOptions(wsId));
  const rosterQuery = useQuery(
    rosterListOptions(wsId, {
      q: urlQ || undefined,
      status: urlStatus || undefined,
      limit: 50,
      offset: 0,
    }),
  );
  const dossierQuery = useQuery(employeeDossierOptions(wsId, urlEmployee));

  const rosterTab = urlTab === "roster";
  const departments = treeQuery.data?.departments ?? [];
  const observedAt = treeQuery.data?.observed_at;
  const employees = rosterQuery.data?.items ?? [];

  const dossierNotFound =
    !!urlEmployee &&
    dossierQuery.isError &&
    dossierQuery.error instanceof ApiError &&
    dossierQuery.error.status === 404;

  const handleSelectEmployee = useCallback(
    (employeeId: string) => {
      writeUrl({ employee: employeeId });
    },
    [writeUrl],
  );

  const handleTabChange = useCallback(
    (nextTab: string) => {
      writeUrl({ tab: nextTab, employee: "" });
    },
    [writeUrl],
  );

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_organization_layout",
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
      <Building2 className="h-4 w-4 text-muted-foreground" />
      <h1 className="text-sm font-semibold">{t(($) => $.page.title)}</h1>
      <div className="ml-auto flex items-center gap-1">
        <Button
          variant={rosterTab ? "ghost" : "secondary"}
          size="sm"
          onClick={() => handleTabChange("")}
          className="gap-1.5"
        >
          <Building2 />
          {t(($) => $.tabs.tree)}
        </Button>
        <Button
          variant={rosterTab ? "secondary" : "ghost"}
          size="sm"
          onClick={() => handleTabChange("roster")}
          className="gap-1.5"
        >
          <UserRound />
          {t(($) => $.tabs.roster)}
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          className="text-muted-foreground"
          onClick={toggleListPanel}
          title={t(($) => $.page.title)}
          aria-label={t(($) => $.page.title)}
        >
          <PanelLeftClose className="h-4 w-4" />
        </Button>
      </div>
    </PageHeader>
  );

  const listBody = rosterTab ? (
    <RosterList
      items={employees}
      total={rosterQuery.data?.total ?? 0}
      loading={rosterQuery.isPending}
      error={rosterQuery.error}
      selectedEmployeeId={urlEmployee}
      q={qDraft}
      status={urlStatus}
      onQChange={setQDraft}
      onStatusChange={(status) => writeUrl({ status })}
      onSelect={(item) => handleSelectEmployee(item.employee_id)}
    />
  ) : (
    <OrgTree
      departments={departments}
      observedAt={observedAt}
      loading={treeQuery.isPending}
      error={treeQuery.error}
      onSelectEmployee={handleSelectEmployee}
    />
  );

  const detailContent = dossierNotFound ? (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <Building2 className="h-10 w-10 text-muted-foreground/30" />
      <p className="text-base font-medium text-foreground">
        {t(($) => $.page.not_found_title)}
      </p>
      <p className="text-sm">{t(($) => $.page.not_found_description)}</p>
    </div>
  ) : dossierQuery.isPending && urlEmployee ? (
    <EmployeeDossierSkeleton />
  ) : dossierQuery.error && urlEmployee ? (
    <div className="flex h-full w-full flex-col justify-center px-4">
      <SourceGapBanner />
    </div>
  ) : dossierQuery.data && urlEmployee ? (
    <EmployeeDossier dossier={dossierQuery.data} />
  ) : (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <Building2 className="h-10 w-10 text-muted-foreground/30" />
      <p className="text-sm">{t(($) => $.page.select_prompt)}</p>
    </div>
  );

  // -- Mobile (≤720px): single column list/detail toggle ---------------------
  if (isCompact) {
    if (urlEmployee) {
      return (
        <div className="flex flex-1 flex-col min-h-0">
          <div className="flex h-12 shrink-0 items-center border-b px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => writeUrl({ employee: "" })}
              className="gap-1.5 text-muted-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {t(($) => $.dossier.back)}
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
        <div className="flex h-full flex-col border-r">
          {listHeader}
          <div className="flex-1 min-h-0 overflow-hidden">{listBody}</div>
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id={DETAIL_PANEL_ID} minSize="40%">
        <div className="flex h-full min-h-0 flex-col">{detailContent}</div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}