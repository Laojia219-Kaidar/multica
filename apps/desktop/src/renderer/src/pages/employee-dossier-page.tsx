import { useParams } from "react-router-dom";
import { EmployeeDossierPage } from "@multica/views/organization";

export function DesktopEmployeeDossierPage() {
  const { employeeId } = useParams<{ employeeId: string }>();
  if (!employeeId) return null;
  return <EmployeeDossierPage employeeId={employeeId} />;
}