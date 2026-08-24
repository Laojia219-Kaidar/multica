import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

/**
 * A2 工作现场 E2E 测试（HIV-1029 验收用例）。
 *
 * 验收清单：
 * - 1440px 视口 → 4 列网格（xl:grid-cols-4）
 * - 卡片互不重叠（HIV-984 展开卡片 bug 不再出现）
 * - 34 人分页：5 页，每页 8 张，末页 2 张
 * - terminal / event_console 互斥
 * - 未匹配 pane 计数但不渲染为员工
 * - 语义化 surface/status token（无硬编码 green/zinc/black）
 */
test.describe("A2 工作现场 (Work Wall)", () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await loginAsDefault(page);
  });

  test("1440px 视口呈现 4×2 几何布局，卡片无重叠", async ({ page }) => {
    await page.goto("/work-wall");
    await page.waitForSelector('[data-testid="work-wall-grid"]');

    const grid = page.locator('[data-testid="work-wall-grid"]');
    await expect(grid).toBeVisible();

    // 获取所有卡片位置
    const cards = page.locator('[data-testid="work-site-card"]');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);

    // 收集每张卡片的 bounding box
    const boxes = [];
    for (let i = 0; i < count; i++) {
      const box = await cards.nth(i).boundingBox();
      if (box) boxes.push(box);
    }

    // 断言没有重叠（HIV-984 回归测试）
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i]!;
        const b = boxes[j]!;
        const overlapX = a.x < b.x + b.width && a.x + a.width > b.x;
        const overlapY = a.y < b.y + b.height && a.y + a.height > b.y;
        expect(overlapX && overlapY).toBe(false);
      }
    }

    // 1440px 视口下，4 列布局意味着首行至少有 4 张卡片（若总数足够）
    if (count >= 4) {
      const firstRowY = boxes[0]?.y ?? 0;
      const firstRowCards = boxes.filter(
        (b) => Math.abs(b.y - firstRowY) < 10,
      );
      expect(firstRowCards.length).toBe(4);
    }
  });

  test("分页：34 人产生 5 页，每页 8 张，末页 2 张", async ({ page }) => {
    await page.route("**/api/work-wall/snapshot", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          Array.from({ length: 34 }, (_, i) => ({
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
          cursor: "cur-1",
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [],
        }),
      }),
    );

    await page.goto("/work-wall");
    await page.waitForSelector('[data-testid="work-wall-pagination"]');

    // 第 1 页：8 张
    await expect(page.locator('[data-testid="work-site-card"]')).toHaveCount(8);
    await expect(page.getByText("显示 1–8 共 34 人")).toBeVisible();

    // 翻到末页（第 5 页）
    const nextBtn = page.getByTestId("work-wall-next-page");
    for (let i = 0; i < 4; i++) {
      await nextBtn.click();
    }

    // 第 5 页：2 张
    await expect(page.locator('[data-testid="work-site-card"]')).toHaveCount(2);
    await expect(page.getByText("显示 33–34 共 34 人")).toBeVisible();
  });

  test("terminal 与 event_console 互斥，terminal 优先", async ({ page }) => {
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
          cursor: "cur-1",
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              pane_id: "ev-1",
              employee_id: "emp-1",
              session_id: "event-console",
              kind: "event_console",
              display_name: "Pixel",
              presence_state: "working",
              work_stage: "coding",
              tail_text: "event log line",
              observed_at: "2026-08-24T12:00:00Z",
              freshness_state: "fresh",
            },
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              pane_id: "tm-1",
              employee_id: "emp-1",
              session_id: "pixel-terminal:0.1",
              kind: "terminal",
              display_name: "Pixel",
              presence_state: "working",
              work_stage: "coding",
              tail_text: "terminal output",
              observed_at: "2026-08-24T12:00:00Z",
              freshness_state: "fresh",
            },
          ],
        }),
      }),
    );

    await page.goto("/work-wall");
    await page.waitForSelector('[data-testid="pane-session-id"]');

    // 只有一个 pane 渲染（terminal 胜出）
    const sessions = page.getByTestId("pane-session-id");
    await expect(sessions).toHaveCount(1);
    await expect(sessions).toContainText("pixel-terminal");
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
          cursor: "cur-1",
          observed_at: "2026-08-24T12:00:00Z",
          event_limit: 100,
          panes: [
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              pane_id: "orphan-1",
              employee_id: "emp-orphan-1",
              session_id: "orphan-sess-1",
              kind: "terminal",
              display_name: "Orphan 1",
              presence_state: "working",
              work_stage: "coding",
              tail_text: "orphan output",
              observed_at: "2026-08-24T12:00:00Z",
              freshness_state: "fresh",
            },
            {
              schema_version: "hivecrew.workwall.a2-pane.v1",
              pane_id: "orphan-2",
              employee_id: "emp-orphan-2",
              session_id: "orphan-sess-2",
              kind: "terminal",
              display_name: "Orphan 2",
              presence_state: "idle",
              work_stage: "none",
              tail_text: "",
              observed_at: "2026-08-24T12:00:00Z",
              freshness_state: "stale",
            },
          ],
        }),
      }),
    );

    await page.goto("/work-wall");
    await page.waitForSelector('[data-testid="work-wall-pagination"]');

    // 只有 1 张员工卡（花名册上只有 Pixel）
    await expect(page.locator('[data-testid="work-site-card"]')).toHaveCount(1);
    // 未匹配数显示
    await expect(page.getByText("2 个未匹配 pane")).toBeVisible();
    // 孤儿 pane 的名字绝不作为员工出现
    await expect(page.getByText("Orphan 1")).not.toBeVisible();
    await expect(page.getByText("Orphan 2")).not.toBeVisible();
  });

  test("使用语义化 surface/status token（无硬编码颜色）", async ({ page }) => {
    await page.goto("/work-wall");
    await page.waitForSelector('[data-testid="work-wall"]');

    const wall = page.locator('[data-testid="work-wall"]');
    const className = await wall.getAttribute("class");
    expect(className).not.toMatch(/bg-green-/);
    expect(className).not.toMatch(/bg-zinc-/);
    expect(className).not.toMatch(/bg-black/);
  });
});
