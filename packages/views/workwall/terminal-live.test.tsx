import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TerminalLiveSection } from "./terminal-live";
import type { TerminalPane } from "@multica/core/api/workwall";

function pane(over: Partial<TerminalPane> = {}): TerminalPane {
  return {
    host: "mac-ultra",
    session_name: "work",
    window_index: 0,
    pane_index: 0,
    current_command: "vim src/app.tsx",
    agent_hint: "pixel-frontend",
    tail_text: "line1\nline2\nline3\nline4\nline5",
    heartbeat_at: new Date(Date.now() - 5_000).toISOString(),
    ...over,
  };
}

describe("TerminalLiveSection", () => {
  it("renders the section header with pane and host counts", () => {
    render(
      <TerminalLiveSection
        panes={[
          pane(),
          pane({ host: "dgx-spark", session_name: "build", window_index: 1, pane_index: 0 }),
        ]}
      />,
    );
    const section = screen.getByTestId("terminal-live-section");
    expect(section.textContent).toContain("Terminal 现场");
    expect(section.textContent).toContain("2 个活跃 pane");
    expect(section.textContent).toContain("2 台主机");
  });

  it("shows empty state when no panes", () => {
    render(<TerminalLiveSection panes={[]} />);
    expect(screen.getByText(/暂无活跃 Terminal 现场/)).toBeDefined();
    expect(screen.queryByTestId("terminal-live-pane")).toBeNull();
  });

  it("renders each pane as a card with session, host, command, hint", () => {
    render(
      <TerminalLiveSection
        panes={[pane({ session_name: "dev-session", agent_hint: "coco-planner" })]}
      />,
    );
    const card = screen.getByTestId("terminal-live-pane");
    expect(card.textContent).toContain("dev-session");
    expect(card.textContent).toContain("mac-ultra:0.0");
    expect(card.textContent).toContain("vim src/app.tsx");
    const hint = screen.getByTestId("terminal-live-agent-hint");
    expect(hint.textContent).toContain("hint: coco-planner");
  });

  it("shows last 3 lines as preview, full tail when expanded", () => {
    render(<TerminalLiveSection panes={[pane()]} />);
    const tail = screen.getByTestId("terminal-live-tail");
    expect(tail.textContent).toBe("line3\nline4\nline5");

    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const fullTail = screen.getByTestId("terminal-live-tail");
    expect(fullTail.textContent).toBe("line1\nline2\nline3\nline4\nline5");
  });

  it("shows idle when current_command is empty", () => {
    render(<TerminalLiveSection panes={[pane({ current_command: "" })]} />);
    const card = screen.getByTestId("terminal-live-pane");
    expect(card.textContent).toContain("idle");
  });

  it("hides agent_hint when empty string", () => {
    render(<TerminalLiveSection panes={[pane({ agent_hint: "" })]} />);
    expect(screen.queryByTestId("terminal-live-agent-hint")).toBeNull();
  });

  it("shows heartbeat age in seconds when recent", () => {
    render(
      <TerminalLiveSection
        panes={[pane({ heartbeat_at: new Date(Date.now() - 30_000).toISOString() })]}
      />,
    );
    const card = screen.getByTestId("terminal-live-pane");
    expect(card.textContent).toMatch(/\d+秒前/);
  });
});

describe("TerminalLiveSection external observation disclaimer", () => {
  it("renders the disclaimer that agent_hint is not authoritative", () => {
    render(<TerminalLiveSection panes={[pane()]} />);
    const disclaimer = screen.getByTestId("terminal-live-disclaimer");
    expect(disclaimer.textContent).toContain("外部观测");
    expect(disclaimer.textContent).toContain("非权威身份绑定");
  });

  it("still shows the disclaimer when no panes exist", () => {
    render(<TerminalLiveSection panes={[]} />);
    const disclaimer = screen.getByTestId("terminal-live-disclaimer");
    expect(disclaimer.textContent).toContain("外部观测");
  });

  it("agent_hint is labeled as hint: not as an authoritative employee name", () => {
    render(<TerminalLiveSection panes={[pane({ agent_hint: "pixel-frontend" })]} />);
    const hint = screen.getByTestId("terminal-live-agent-hint");
    expect(hint.textContent).toMatch(/^hint: /);
    expect(hint.textContent).not.toContain("员工");
    expect(hint.textContent).not.toContain("employee");
  });
});
