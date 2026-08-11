"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Building2 } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { ApiError } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { employeeDossierOptions } from "./organization-queries";
import {
  EmployeeDossier,
  EmployeeDossierSkeleton,
} from "./employee-dossier";
import { SourceGapBanner } from "./source-gap";

/**
 * Standalone employee dossier route (`/{slug}/organization/employees/:id`).
 * Deep-link entry straight into the dossier detail — the same read model the
 * Organization page renders inline, so the two surfaces never diverge.
 */
export function EmployeeDossierPage({ employeeId }: { employeeId: string }) {
  const { t } = useT("organization");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { push } = useNavigation();
  const query = useQuery(employeeDossierOptions(wsId, employeeId));

  const notFound =
    query.isError &&
    query.error instanceof ApiError &&
    query.error.status === 404;

  let content: React.ReactNode;
  if (query.isPending) {
    content = <EmployeeDossierSkeleton />;
  } else if (notFound) {
    content = (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
        <Building2 className="h-10 w-10 text-muted-foreground/30" />
        <p className="text-base font-medium text-foreground">
          {t(($) => $.page.not_found_title)}
        </p>
        <p className="text-sm">{t(($) => $.page.not_found_description)}</p>
      </div>
    );
  } else if (query.error || !query.data) {
    content = (
      <div className="flex h-full w-full flex-col justify-center px-4">
        <SourceGapBanner />
      </div>
    );
  } else {
    content = <EmployeeDossier dossier={query.data} />;
  }

  return (
    <div className="flex flex-1 flex-col min-h-0">
      <div className="flex h-12 shrink-0 items-center border-b px-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => push(wsPaths.organization())}
          className="gap-1.5 text-muted-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          {t(($) => $.dossier.back)}
        </Button>
      </div>
      <div className="flex-1 min-h-0 overflow-hidden">{content}</div>
    </div>
  );
}