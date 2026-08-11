export interface HiveCrewWorkContext {
  workspaceSlug: string;
  employee_id: string;
  identity_binding_id: string;
  agent_id: string;
  work_order_source_ref: string;
  session_id: string;
}

const REQUIRED_QUERY_KEYS = [
  "employee_id",
  "identity_binding_id",
  "agent_id",
  "work_order_source_ref",
  "session_id",
] as const satisfies ReadonlyArray<keyof HiveCrewWorkContext>;

type RequiredQueryKey = (typeof REQUIRED_QUERY_KEYS)[number];

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const WORK_ORDER_SOURCE_REF_PATTERN =
  /^hive:\/\/hivecosm\/delivery\/project\/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}\/work-order\/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$/;

function requireNonEmpty(value: unknown, key: keyof HiveCrewWorkContext): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`Missing required ${key}`);
  }
  return value;
}

function requireCanonicalUuid(value: string, key: "agent_id" | "session_id"): void {
	if (!UUID_PATTERN.test(value)) {
		throw new Error(`${key} must be a canonical UUID`);
	}
}

function requireWorkOrderSourceRef(sourceRef: string): void {
	if (
    sourceRef.trim() !== sourceRef ||
    !WORK_ORDER_SOURCE_REF_PATTERN.test(sourceRef)
  ) {
		throw new Error(
      "work_order_source_ref must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}",
    );
	}
}

function readRequiredQueryValue(
  searchParams: URLSearchParams,
  key: RequiredQueryKey,
): string {
  const values = searchParams.getAll(key);
  if (values.length === 0) {
    throw new Error(`Missing required ${key}`);
  }

  if (values.some((value) => value !== values[0])) {
    throw new Error(`Conflicting ${key} values`);
  }

  return requireNonEmpty(values[0]!, key);
}

export function buildHiveCrewWorkContextUrl(
  context: HiveCrewWorkContext,
): string {
  const workspaceSlug = requireNonEmpty(context?.workspaceSlug, "workspaceSlug");
  const values = Object.fromEntries(
    REQUIRED_QUERY_KEYS.map((key) => [key, requireNonEmpty(context?.[key], key)]),
  ) as Record<RequiredQueryKey, string>;

	requireCanonicalUuid(values.agent_id, "agent_id");
	requireCanonicalUuid(values.session_id, "session_id");
	requireWorkOrderSourceRef(values.work_order_source_ref);

  const query = REQUIRED_QUERY_KEYS.map(
    (key) => `${key}=${encodeURIComponent(values[key])}`,
  ).join("&");

  return `/${encodeURIComponent(workspaceSlug)}/chat?${query}`;
}

export function parseHiveCrewWorkContextUrl(url: string): HiveCrewWorkContext {
  let parsedUrl: URL;
  try {
    parsedUrl = new URL(url, "https://hivecrew.invalid");
  } catch {
    throw new Error("Invalid HiveCrew work-context URL");
  }

  const segments = parsedUrl.pathname.split("/").filter(Boolean);
  if (segments.length < 2) {
    throw new Error("Missing required workspaceSlug");
  }
  if (segments.length !== 2 || segments[1] !== "chat") {
    throw new Error("Invalid HiveCrew work-context route");
  }

  let workspaceSlug: string;
  try {
    workspaceSlug = decodeURIComponent(segments[0]!);
  } catch {
    throw new Error("Invalid workspaceSlug encoding");
  }
  workspaceSlug = requireNonEmpty(workspaceSlug, "workspaceSlug");

  const employeeId = readRequiredQueryValue(
    parsedUrl.searchParams,
    "employee_id",
  );
  const identityBindingId = readRequiredQueryValue(
    parsedUrl.searchParams,
    "identity_binding_id",
  );
  const agentId = readRequiredQueryValue(parsedUrl.searchParams, "agent_id");
  const workOrderSourceRef = readRequiredQueryValue(
    parsedUrl.searchParams,
    "work_order_source_ref",
  );
  const sessionId = readRequiredQueryValue(parsedUrl.searchParams, "session_id");

	requireCanonicalUuid(agentId, "agent_id");
	requireCanonicalUuid(sessionId, "session_id");
	requireWorkOrderSourceRef(workOrderSourceRef);

  return {
    workspaceSlug,
    employee_id: employeeId,
    identity_binding_id: identityBindingId,
    agent_id: agentId,
    work_order_source_ref: workOrderSourceRef,
    session_id: sessionId,
  };
}
