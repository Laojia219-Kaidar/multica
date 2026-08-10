import { describe, expect, it } from "vitest";

import { isOfficialMarketingHost } from "./public-host";

describe("isOfficialMarketingHost", () => {
  it.each(["crew.hivecosm.test", "CREW.HIVECOSM.TEST", "crew.hivecosm.test."])(
    "recognizes %s as an official marketing host",
    (host) => {
      expect(
        isOfficialMarketingHost(host, "https://crew.hivecosm.test"),
      ).toBe(true);
    },
  );

  it.each(["app.hivecosm.test", "api.hivecosm.test", "localhost", "multica.ai"])(
    "does not treat %s as the public marketing host",
    (host) => {
      expect(
        isOfficialMarketingHost(host, "https://crew.hivecosm.test"),
      ).toBe(false);
    },
  );

  it("has no implicit inherited public host", () => {
    expect(isOfficialMarketingHost("multica.ai", undefined)).toBe(false);
  });
});
