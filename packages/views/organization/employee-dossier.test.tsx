// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../test/i18n";
import { EmployeeDossier } from "./employee-dossier";
import type { CompanyOpsEmployeeDossier } from "@multica/core/types";

const agentUuid = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/acme/agents/${id}`,
    chat: () => "/acme/chat",
    organization: () => "/acme/organization",
  }),
}));

function dossier(overrides: Partial<CompanyOpsEmployeeDossier>): CompanyOpsEmployeeDossier {
  return {
    schema_version: "hivecrew.organization.v1",
    employee_id: "DE-CEO-001",
    display_name: "Coco",
    department_ref: "hivecosm://departments/DE-DEPT-ENG",
    position_ref: "hivecosm://positions/POS-ENG-LEAD",
    workforce_agent_id: "KT-002",
    hivecrew_agent_id: agentUuid,
    bindings: [
      {
        identity_binding_id: "BIND-001",
        employee_ref: "hivecosm://employees/DE-CEO-001",
        workforce_agent_id: "KT-002",
        agent_ref: `/api/agents/${agentUuid}`,
        active: true,
        authority: {
          kind: "IdentityBinding",
          source_ref: "hivecosm://identity-bindings/BIND-001",
          revision: "rev-1",
          content_digest: "sha256:abc",
          freshness: "current",
        },
      },
    ],
    binding_state: "available",
    local_agent: {
      id: agentUuid,
      name: "Coco",
      status: "idle",
      runtime_mode: "local",
      model: "deepseek-v4-flash",
      authority: {
        kind: "Agent",
        source_ref: `/api/agents/${agentUuid}`,
        revision: "rev-1",
        content_digest: "sha256:abc",
        freshness: "current",
      },
    },
    authority: {
      kind: "Employee",
      source_ref: "hivecosm://employees/DE-CEO-001",
      revision: "rev-1",
      content_digest: "sha256:abc",
      freshness: "current",
    },
    ...overrides,
  };
}

describe("EmployeeDossier", () => {
  it("renders identity, all bindings and the authority basis", () => {
    renderWithI18n(<EmployeeDossier dossier={dossier({})} />);
    expect(screen.getAllByText("DE-CEO-001").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Coco").length).toBeGreaterThan(0);
    expect(screen.getByText("hivecosm://departments/DE-DEPT-ENG")).toBeInTheDocument();
    expect(screen.getByText("BIND-001")).toBeInTheDocument();
    expect(screen.getByText(`/api/agents/${agentUuid}`)).toBeInTheDocument();
  });

  it("constructs the exact Agent settings and Chat links when available", () => {
    renderWithI18n(<EmployeeDossier dossier={dossier({})} />);
    const agentLink = screen.getByRole("link", { name: "Open agent settings" });
    expect(agentLink).toHaveAttribute("href", `/acme/agents/${agentUuid}`);
    const chatLink = screen.getByRole("link", { name: "Open chat to assign" });
    expect(chatLink).toHaveAttribute("href", `/acme/chat?agent=${agentUuid}`);
  });

  it.each([
    ["none", "No execution identity binding"],
    ["inactive_only", "Binding inactive"],
    ["multiple_active_conflict", "Binding conflict"],
    ["local_agent_missing_or_invalid", "Execution identity resolution failed"],
    ["source_gap", "Authority unavailable"],
  ] as const)(
    "fail-closes with an explainable state for %s — no links",
    (state, stateLabel) => {
      renderWithI18n(
        <EmployeeDossier
          dossier={dossier({ binding_state: state, local_agent: null })}
        />,
      );
      expect(screen.getByText(stateLabel)).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "Open agent settings" })).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "Open chat to assign" })).not.toBeInTheDocument();
      expect(screen.getByText("No execution identity is available.")).toBeInTheDocument();
    },
  );

  it("shows the conflict reason without picking a candidate for multiple_active_conflict", () => {
    renderWithI18n(
      <EmployeeDossier
        dossier={dossier({
          binding_state: "multiple_active_conflict",
          local_agent: null,
          bindings: [
            ...dossier({}).bindings,
            {
              identity_binding_id: "BIND-002",
              employee_ref: "hivecosm://employees/DE-CEO-001",
              workforce_agent_id: "EXT-9",
              agent_ref: "/api/agents/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
              active: true,
              authority: {
                kind: "IdentityBinding",
                source_ref: "hivecosm://identity-bindings/BIND-002",
                revision: "rev-1",
                content_digest: "sha256:def",
                freshness: "current",
              },
            },
          ],
        })}
      />,
    );
    expect(screen.getByText("Binding conflict")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Multiple active bindings exist. Resolve the conflict in HiveCosm before assigning.",
      ),
    ).toBeInTheDocument();
    // Both candidate bindings are listed; neither is silently chosen.
    expect(screen.getByText("BIND-001")).toBeInTheDocument();
    expect(screen.getByText("BIND-002")).toBeInTheDocument();
  });

  it("shows inactive bindings greyed as history with a note", () => {
    renderWithI18n(
      <EmployeeDossier
        dossier={dossier({
          binding_state: "inactive_only",
          local_agent: null,
          bindings: [
            {
              ...dossier({}).bindings[0]!,
              active: false,
            },
          ],
        })}
      />,
    );
    expect(screen.getByText("Inactive")).toBeInTheDocument();
    expect(
      screen.getByText("Inactive binding — historical revision only."),
    ).toBeInTheDocument();
  });

  it("never renders a dispatch entry when the workforce agent id is missing", () => {
    renderWithI18n(
      <EmployeeDossier
        dossier={dossier({ binding_state: "none", workforce_agent_id: null, local_agent: null })}
      />,
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});