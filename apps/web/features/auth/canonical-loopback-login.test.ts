import { describe, expect, it } from "vitest";
import { canonicalLoopbackLoginUrl } from "./canonical-loopback-login";

describe("canonicalLoopbackLoginUrl", () => {
  it("aligns a 127 candidate page to a localhost API without changing its frontend port or path", () => {
    expect(canonicalLoopbackLoginUrl(
      "http://127.0.0.1:13512/login?next=%2Facme%2Fworkflow",
      "http://localhost:18592",
    )).toBe("http://localhost:13512/login?next=%2Facme%2Fworkflow");
  });

  it("does not redirect same-host, non-loopback, or malformed URLs", () => {
    expect(canonicalLoopbackLoginUrl("http://localhost:13512/login", "http://localhost:18592")).toBeNull();
    expect(canonicalLoopbackLoginUrl("https://app.example/login", "https://api.example")).toBeNull();
    expect(canonicalLoopbackLoginUrl("not a URL", "http://localhost:18592")).toBeNull();
  });
});
