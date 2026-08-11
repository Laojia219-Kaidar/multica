"use client";

import { use } from "react";
import { EmployeeDossierPage } from "@multica/views/organization";

export default function EmployeeDossierRoute({
  params,
}: {
  params: Promise<{ employeeId: string }>;
}) {
  const { employeeId } = use(params);
  return <EmployeeDossierPage employeeId={employeeId} />;
}