import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

/**
 * A2 工作现场 E2E 测试（HIV-1030 返修验收）。
 *
 * 验收清单：
 * - 1440px 视口 → 4 列网格（xl:grid-cols-4）
 * - 8 张卡片，4 个 x 位置，2 个 y 位置，无重叠
 * - 分页（2 页，末页 2 张 = 10 人）
 * - terminal 与事件台互斥：terminal 仅在
 *   surface_kind=terminal + session_id 非空 + TerminalPane.session_name 精确匹配时显示
 * - tail_text 只来自 TerminalPane
 * - 未匹配 pane 计数但不渲染为员工
 * - 语义化 surface/status token（无硬编码 green/zinc/black）
 * - loginAsDefault 返回 workspace slug
 */
test.describe("A2 工作现场 (Work Wall)", () => {
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    workspaceSlug = await loginAsDefault(page);
  });

  test("1440px 视口 8 张卡片 4×2 几何布局，无重叠", async ({ page }) => {
    // 10 employees → page 1 = 8 cards in 4×2 layout
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          Array.from({ length: 10 }, (_, i) => ({
            schema_version: "hivecrew.employee-live-activity.v1",
            workspace_id: "ws-1",
            employee_id: `emp-${i + 1}`,
            agent_id: `agt-${i + 1}`,
            display_name: `员工${i + 1}`,
            presence_state: i % 3 === 0 ? "working" : "idle",
            work_stage: "coding",
            recent_events: [],
            source_refs: [`agent://agt-${i + 1}`],
            observed_at: "2026-08-24T12:00:00Z",
            freshness_state: "fresh",
          })),
        ),
      }),
    );

    await page.route("**/api/work-wall/a2/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          schema_version: "hivecrew.workwall.a2-snapshot.v1",
          workspace_id: "ws-1",
          cursor: "sha256:" + "a".repeat(64),
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [],
        }),
      }),
    );

    await page.route("**/api/work-wall/terminal-presence", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      }),
    );

    await page.goto(`/${workspaceSlug}/work-wall`);
    await page.waitForSelector('[data-testid="work-wall-grid"]');

    const grid = page.locator('[data-testid="work-wall-grid"]');
    await expect(grid).toBeVisible();

    // Page 1 should have exactly 8 cards
    const cards = page.locator('[data-testid="work-site-card"]');
    const count = await cards.count();
    expect(count).toBe(8);

    // Collect bounding boxes
    const boxes = [];
    for (let i = 0; i < count; i++) {
      const box = await cards.nth(i).boundingBox();
      if (box) boxes.push(box);
    }
    expect(boxes.length).toBe(8);

    // No overlap (HIV-984 regression)
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i]!;
        const b = boxes[j]!;
        const overlapX = a.x < b.x + b.width && a.x + a.width > b.x;
        const overlapY = a.y < b.y + b.height && a.y + a.height > b.y;
        expect(overlapX && overlapY).toBe(false);
      }
    }

    // 4 distinct x positions (4 columns)
    const xPositions = new Set(boxes.map((b) => Math.round(b.x)));
    expect(xPositions.size).toBe(4);

    // 2 distinct y positions (2 rows)
    const yPositions = new Set(boxes.map((b) => Math.round(b.y)));
    expect(yPositions.size).toBe(2);

    // Pagination: 10 employees = 2 pages, page 2 has 2 cards
    await expect(page.locator('[data-testid="work-wall-pagination"]')).toBeVisible();
    await expect(page.getByText("1 / 2")).toBeVisible();

    const nextBtn = page.getByTestId("work-wall-next-page");
    await nextBtn.click();

    // Final page (page 2) has 2 cards
    await expect(page.locator('[data-testid="work-site-card"]')).toHaveCount(2);
    await expect(page.getByText("显示 9–10 共 10 人")).toBeVisible();
    await expect(page.getByText("2 / 2")).toBeVisible();
  });

  test("terminal 与事件台互斥，tail_text 仅来自 TerminalPane", async ({ page }) => {
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            schema_version: "hivecrew.employee-live-activity.v1",
            workspace_id: "ws-1",
            employee_id: "emp-1",
            agent_id: "agt-1",
            display_name: "Pixel",
            presence_state: "working",
            work_stage: "coding",
            recent_events: [],
            source_refs: ["agent://agt-1"],
            observed_at: "2026-08-24T12:00:00Z",
            freshness_state: "fresh",
          },
        ]),
      }),
    );

    await page.route("**/api/work-wall/a2/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          schema_version: "hivecrew.workwall.a2-snapshot.v1",
          workspace_id: "ws-1",
          cursor: "sha256:" + "a".repeat(64),
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              workspace_id: "ws-1",
              work_ref: "wr-1",
              source_event_id: "evt-1",
              employee_id: "emp-1",
              session_id: "pixel-terminal:0.1",
              execution_state: "active",
              working: true,
              surface_kind: "terminal",
              freshness_state: "fresh",
              observed_at: "2026-08-24T12:00:00Z",
              activity_summary: "执行中",
              source_refs: ["work_event://evt-1"],
            },
          ],
        }),
      }),
    );

    await page.route("**/api/work-wall/terminal-presence", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            host: "pixel-main",
            session_name: "pixel-terminal:0.1",
            window_index: 0,
            pane_index: 1,
            current_command: "npm test",
            agent_hint: "Pixel",
            tail_text: "REAL_TERMINAL_OUTPUT\nPASS",
            heartbeat_at: "2026-08-24T12:00:00Z",
          },
        ]),
      }),
    );

    await page.goto(`/${workspaceSlug}/work-wall`);
    await page.waitForSelector('[data-testid="pane-tail"]');

    // Terminal tail is visible
    const tail = page.locator('[data-testid="pane-tail"]');
    await expect(tail).toBeVisible();
    await expect(tail).toContainText("REAL_TERMINAL_OUTPUT");

    // Session label shows the terminal session_name
    const sessionId = page.locator('[data-testid="pane-session-id"]');
    await expect(sessionId).toContainText("pixel-terminal:0.1");
  });

  test("surface_kind=event_console 时不显示终端（事件台替代）", async ({ page }) => {
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            schema_version: "hivecrew.employee-live-activity.v1",
            workspace_id: "ws-1",
            employee_id: "emp-1",
            agent_id: "agt-1",
            display_name: "Pixel",
            presence_state: "working",
            work_stage: "coding",
            recent_events: [],
            source_refs: ["agent://agt-1"],
            observed_at: "2026-08-24T12:00:00Z",
            freshness_state: "fresh",
          },
        ]),
      }),
    );

    await page.route("**/api/work-wall/a2/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          schema_version: "hivecrew.workwall.a2-snapshot.v1",
          workspace_id: "ws-1",
          cursor: "sha256:" + "a".repeat(64),
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              workspace_id: "ws-1",
              work_ref: "wr-1",
              source_event_id: "evt-1",
              employee_id: "emp-1",
              execution_state: "active",
              working: false,
              surface_kind: "event_console",
              freshness_state: "fresh",
              observed_at: "2026-08-24T12:00:00Z",
              activity_summary: "执行中",
              source_refs: ["work_event://evt-1"],
            },
          ],
        }),
      }),
    );

    await page.route("**/api/work-wall/terminal-presence", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            host: "pixel-main",
            session_name: "other-session:0.1",
            window_index: 0,
            pane_index: 1,
            current_command: "ls",
            agent_hint: "Other",
            tail_text: "this should not appear",
            heartbeat_at: "2026-08-24T12:00:00Z",
          },
        ]),
      }),
    );

    await page.goto(`/${workspaceSlug}/work-wall`);
    await page.waitForSelector('[data-testid="work-site-card"]');

    // No terminal tail — event console shown
    const tail = page.locator('[data-testid="pane-tail"]');
    await expect(tail).toHaveCount(0);

    // Event console visible
    await expect(page.getByText("事件台")).toBeVisible();
    await expect(page.getByText("执行中")).toBeVisible();
  });

  test("未匹配 pane 被计数但不渲染为员工卡片", async ({ page }) => {
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            schema_version: "hivecrew.employee-live-activity.v1",
            workspace_id: "ws-1",
            employee_id: "emp-1",
            agent_id: "agt-1",
            display_name: "Pixel",
            presence_state: "working",
            work_stage: "coding",
            recent_events: [],
            source_refs: ["agent://agt-1"],
            observed_at: "2026-08-24T12:00:00Z",
            freshness_state: "fresh",
          },
        ]),
      }),
    );

    await page.route("**/api/work-wall/a2/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          schema_version: "hivecrew.workwall.a2-snapshot.v1",
          workspace_id: "ws-1",
          cursor: "sha256:" + "a".repeat(64),
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              workspace_id: "ws-1",
              work_ref: "orphan-wr-1",
              source_event_id: "evt-o1",
              employee_id: "emp-orphan-1",
              execution_state: "active",
              working: false,
              surface_kind: "event_console",
              freshness_state: "fresh",
              observed_at: "2026-08-24T12:00:00Z",
              source_refs: ["work_event://evt-o1"],
            },
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              workspace_id: "ws-1",
              work_ref: "orphan-wr-2",
              source_event_id: "evt-o2",
              employee_id: "emp-orphan-2",
              execution_state: "replay",
              working: false,
              surface_kind: "event_console",
              freshness_state: "stale",
              observed_at: "2026-08-24T12:00:00Z",
              source_refs: ["work_event://evt-o2"],
            },
          ],
        }),
      }),
    );

    await page.route("**/api/work-wall/terminal-presence", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      }),
    );

    await page.goto(`/${workspaceSlug}/work-wall`);
    await page.waitForSelector('[data-testid="work-wall-pagination"]');

    // Only 1 employee card (roster has only Pixel)
    await expect(page.locator('[data-testid="work-site-card"]')).toHaveCount(1);
    // Unmatched count shown
    await expect(page.getByText("2 个未匹配 pane")).toBeVisible();
    // Orphan panes never render as employees
    await expect(page.getByText(/orphan/i)).not.toBeVisible();
  });

  test("使用语义化 surface/status token（无硬编码颜色）", async ({ page }) => {
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            schema_version: "hivecrew.employee-live-activity.v1",
            workspace_id: "ws-1",
            employee_id: "emp-1",
            agent_id: "agt-1",
            display_name: "Pixel",
            presence_state: "working",
            work_stage: "coding",
            recent_events: [],
            source_refs: ["agent://agt-1"],
            observed_at: "2026-08-24T12:00:00Z",
            freshness_state: "fresh",
          },
        ]),
      }),
    );

    await page.route("**/api/work-wall/a2/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          schema_version: "hivecrew.workwall.a2-snapshot.v1",
          workspace_id: "ws-1",
          cursor: "sha256:" + "a".repeat(64),
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [],
        }),
      }),
    );

    await page.route("**/api/work-wall/terminal-presence", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      }),
    );

    await page.goto(`/${workspaceSlug}/work-wall`);
    await page.waitForSelector('[data-testid="work-wall"]');

    const wall = page.locator('[data-testid="work-wall"]');
    const className = await wall.getAttribute("class");
    expect(className).not.toMatch(/bg-green-/);
    expect(className).not.toMatch(/bg-zinc-/);
    expect(className).not.toMatch(/bg-black/);
  });
});
