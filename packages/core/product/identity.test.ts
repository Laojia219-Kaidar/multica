import { describe, expect, it } from "vitest";
import {
  HIVE_CREW_BROWSER_DESCRIPTION,
  HIVE_CREW_BROWSER_TITLE,
  HIVE_CREW_DESKTOP_APP_ID,
  HIVE_CREW_DESKTOP_PROTOCOL,
  HIVE_CREW_LEGACY_DESKTOP_PROTOCOLS,
  HIVE_CREW_LOCAL_ENDPOINTS,
  HIVE_CREW_PRODUCT,
  HIVE_CREW_PRODUCT_NAME,
  HIVE_CREW_WORKSPACE_FALLBACK_HOST,
} from "./identity";

describe("HiveCrew product identity", () => {
  it("exposes one first-party product identity", () => {
    expect(HIVE_CREW_PRODUCT).toEqual({
      name: "HiveCrew",
      companyName: "HiveCosm",
      displayNameZh: "蜂巢数字团队",
      tagline: "Digital employee collaboration and execution system",
    });
    expect(HIVE_CREW_PRODUCT_NAME).toBe("HiveCrew");
    expect(HIVE_CREW_BROWSER_TITLE).toBe(
      "HiveCrew · HiveCosm 数字员工协同工作台",
    );
    expect(HIVE_CREW_BROWSER_DESCRIPTION).toBe(
      "HiveCosm 的数字员工协同、项目派工与成果执行工作台。",
    );
    expect(HIVE_CREW_DESKTOP_APP_ID).toBe("com.hivecosm.hivecrew");
    expect(HIVE_CREW_DESKTOP_PROTOCOL).toBe("hivecrew");
    expect(HIVE_CREW_WORKSPACE_FALLBACK_HOST).toBe("hivecrew.local");
  });

  it("keeps new-install defaults local and legacy protocols read-only", () => {
    expect(HIVE_CREW_LOCAL_ENDPOINTS).toEqual({
      apiUrl: "http://127.0.0.1:8080",
      wsUrl: "ws://127.0.0.1:8080/ws",
      appUrl: "http://127.0.0.1:3000",
    });
    expect(HIVE_CREW_LEGACY_DESKTOP_PROTOCOLS).toEqual(["multica"]);
    expect(HIVE_CREW_DESKTOP_PROTOCOL).not.toBe(
      HIVE_CREW_LEGACY_DESKTOP_PROTOCOLS[0],
    );
  });
});
