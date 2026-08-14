const { chromium } = require("playwright");
const fs = require("fs");

const BASE = "http://127.0.0.1:3000";
const token = fs.readFileSync("/tmp/e2e_token.txt", "utf8").trim();

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" });
  const ctx = await browser.newContext();
  await ctx.addCookies([
    { name: "multica_auth", value: token, domain: "127.0.0.1", path: "/", httpOnly: true, sameSite: "Lax" },
  ]);

  const results = {};
  const page = await ctx.newPage();

  const check = async (name, url, waitFor) => {
    try {
      const resp = await page.goto(BASE + url, { waitUntil: "domcontentloaded", timeout: 20000 });
      await page.waitForTimeout(2500);
      const body = await page.evaluate(() => document.body.innerText);
      const has = waitFor ? body.includes(waitFor) : true;
      const title = await page.title();
      results[name] = { status: resp.status(), hasExpected: has, title: title.slice(0,80), bodyLen: body.length };
    } catch (e) {
      results[name] = { error: String(e).slice(0,120) };
    }
  };

  // VC-12 scenario surfaces (browser, real DOM)
  await check("projects", "/hivecosm/projects", "HIVECREW");
  await check("project-detail-P01", "/hivecosm/projects/3b0330e7-a2da-4f41-94ab-61c911af2820", "项目控制");
  await check("issues", "/hivecosm/issues", "HIV");
  await check("review-queue", "/hivecosm/issues/review", "review");
  await check("work-wall", "/hivecosm/work-wall", "工作现场");
  await check("usage", "/hivecosm/usage", "Usage");
  await check("outcomes", "/hivecosm/outcomes", "Outcome");

  console.log(JSON.stringify(results, null, 2));
  await browser.close();
})();
