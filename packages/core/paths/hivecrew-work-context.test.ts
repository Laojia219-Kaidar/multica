import { describe, expect, it } from "vitest";
import {
  buildHiveCrewWorkContextUrl,
  parseHiveCrewWorkContextUrl,
  type HiveCrewWorkContext,
} from "./hivecrew-work-context";

const AGENT_UUID = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const SESSION_UUID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001";
const SOURCE_REF =
  "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001";

const context: HiveCrewWorkContext = {
  workspaceSlug: "owner-space",
  employee_id: "employee-01JOWNER",
  identity_binding_id: "binding-01JOWNER",
  agent_id: AGENT_UUID,
  work_order_source_ref: SOURCE_REF,
	session_id: SESSION_UUID,
};

const EXPECTED_URL =
  "/owner-space/chat" +
  "?employee_id=employee-01JOWNER" +
  "&identity_binding_id=binding-01JOWNER" +
  `&agent_id=${AGENT_UUID}` +
	"&work_order_source_ref=hive%3A%2F%2Fhivecosm%2Fdelivery%2Fproject%2FPRJ-HIVECREW-P2%2Fwork-order%2FWO-P2-001" +
	`&session_id=${SESSION_UUID}`;

const REQUIRED_KEYS = [
  "workspaceSlug",
  "employee_id",
  "identity_binding_id",
  "agent_id",
  "work_order_source_ref",
  "session_id",
] as const satisfies ReadonlyArray<keyof HiveCrewWorkContext>;

describe("HiveCrew owner work-context URL", () => {
  it("round-trips every stable identity and execution id without substitution", () => {
    const url = buildHiveCrewWorkContextUrl(context);

    expect(url).toBe(EXPECTED_URL);
    expect(parseHiveCrewWorkContextUrl(url)).toEqual(context);
  });

	it("percent-encodes the complete canonical source ref", () => {
    const url = buildHiveCrewWorkContextUrl(context);
    const parsedUrl = new URL(url, "https://hivecrew.test");

		expect(url).toContain(
			"work_order_source_ref=hive%3A%2F%2Fhivecosm%2Fdelivery%2Fproject%2FPRJ-HIVECREW-P2%2Fwork-order%2FWO-P2-001",
    );
    expect(parsedUrl.searchParams.get("work_order_source_ref")).toBe(SOURCE_REF);
    expect(parsedUrl.hash).toBe("");
	});

	it.each([
		"hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001?revision=2",
		"hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001#draft",
		"hive://owner@hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
		"hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/WO-P2-001",
		"hivecosm://work-orders/WO-P2-001",
		" hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
		"WO-P2-001",
	])("rejects non-canonical WorkOrder source ref %s", (sourceRef) => {
		expect(() =>
			buildHiveCrewWorkContextUrl({
				...context,
				work_order_source_ref: sourceRef,
			}),
		).toThrow(/work_order_source_ref/i);
	});

	it.each(["session-01JRUN", "D34DB33F-4EF7-4FE1-A32D-8F24C57B07B1"])(
		"rejects non-canonical session UUID %s",
		(sessionId) => {
			expect(() =>
				buildHiveCrewWorkContextUrl({ ...context, session_id: sessionId }),
			).toThrow(/session_id/i);
		},
	);

  it.each(REQUIRED_KEYS)(
    "fails closed when builder input omits required %s",
    (key) => {
      const incomplete = { ...context } as Record<string, unknown>;
      delete incomplete[key];

      expect(() =>
        buildHiveCrewWorkContextUrl(incomplete as unknown as HiveCrewWorkContext),
      ).toThrow(new RegExp(key, "i"));
    },
  );

  it.each(REQUIRED_KEYS)(
    "fails closed when parsed URL omits required %s",
    (key) => {
      const parsedUrl = new URL(EXPECTED_URL, "https://hivecrew.test");
      if (key === "workspaceSlug") {
        parsedUrl.pathname = "/chat";
      } else {
        parsedUrl.searchParams.delete(key);
      }

      expect(() =>
        parseHiveCrewWorkContextUrl(`${parsedUrl.pathname}${parsedUrl.search}`),
      ).toThrow(new RegExp(key, "i"));
    },
  );

  it.each([
    "employee_id",
    "identity_binding_id",
    "agent_id",
    "work_order_source_ref",
    "session_id",
  ])(
    "rejects conflicting duplicate %s values instead of taking the first item",
    (key) => {
      const parsedUrl = new URL(EXPECTED_URL, "https://hivecrew.test");
      parsedUrl.searchParams.append(key, `${key}-conflict`);

      expect(() =>
        parseHiveCrewWorkContextUrl(`${parsedUrl.pathname}${parsedUrl.search}`),
      ).toThrow(new RegExp(`(${key}.*conflict|conflict.*${key})`, "i"));
    },
  );

  it("does not accept display names as employee or agent identity fallbacks", () => {
    const parsedUrl = new URL(EXPECTED_URL, "https://hivecrew.test");
    parsedUrl.searchParams.delete("employee_id");
    parsedUrl.searchParams.delete("agent_id");
    parsedUrl.searchParams.set("employee_name", "William Owner");
    parsedUrl.searchParams.set("agent_name", "Atlas Operator");

    expect(() =>
      parseHiveCrewWorkContextUrl(`${parsedUrl.pathname}${parsedUrl.search}`),
    ).toThrow(/employee_id|agent_id/i);
  });
});
