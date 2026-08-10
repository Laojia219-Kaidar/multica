export const HIVE_CREW_PRODUCT_NAME = "HiveCrew" as const;

export const HIVE_CREW_PRODUCT = Object.freeze({
  name: HIVE_CREW_PRODUCT_NAME,
  companyName: "HiveCosm",
  displayNameZh: "蜂巢数字团队",
  tagline: "Digital employee collaboration and execution system",
});

export const HIVE_CREW_DESKTOP_APP_ID = "com.hivecosm.hivecrew" as const;
export const HIVE_CREW_DESKTOP_PROTOCOL = "hivecrew" as const;

// Accepted only while existing installations migrate. New HiveCrew surfaces
// never emit a legacy protocol URL.
export const HIVE_CREW_LEGACY_DESKTOP_PROTOCOLS = ["multica"] as const;

export const HIVE_CREW_WORKSPACE_FALLBACK_HOST = "hivecrew.local" as const;

// A missing packaged configuration must remain local-first. It must never
// connect an independently developed HiveCrew build to a former cloud service.
export const HIVE_CREW_LOCAL_ENDPOINTS = Object.freeze({
  apiUrl: "http://127.0.0.1:8080",
  wsUrl: "ws://127.0.0.1:8080/ws",
  appUrl: "http://127.0.0.1:3000",
});
