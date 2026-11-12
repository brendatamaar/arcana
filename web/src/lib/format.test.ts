import { describe, expect, it } from "vitest";
import { deadlines, describeReason, formatDuration, formatTime } from "./format";

describe("formatDuration", () => {
  it.each([
    [0, "0s"],
    [59, "59s"],
    [61, "1m 01s"],
    [3600, "1h 00m"],
    [7385, "2h 03m"],
    [-5, "0s"],
  ])("%d seconds is %s", (input, want) => {
    expect(formatDuration(input)).toBe(want);
  });
});

describe("deadlines", () => {
  const now = Date.parse("2026-11-12T12:00:00Z");
  it("reports the idle deadline from the last activity and never beyond the TTL", () => {
    const sb = {
      expires_at: "2026-11-12T12:30:00Z",
      last_activity_at: "2026-11-12T11:55:00Z",
      idle_timeout_seconds: 600,
    };
    expect(deadlines(sb, now)).toEqual({ ttl: 1800, idle: 300 });
    expect(deadlines({ ...sb, expires_at: "2026-11-12T12:02:00Z" }, now)).toEqual({
      ttl: 120,
      idle: 120,
    });
  });
  it("disables the idle countdown when the sandbox has no idle timeout", () => {
    expect(
      deadlines({ expires_at: "2026-11-12T12:30:00Z", idle_timeout_seconds: 0 }, now).idle,
    ).toBeNull();
  });
});

describe("describeReason / formatTime", () => {
  it("explains known reasons and shows unknown ones verbatim", () => {
    expect(describeReason("ttl")).toContain("time-to-live");
    expect(describeReason("exec_deadline")).toContain("deadline");
    expect(describeReason("something_new")).toBe("something_new");
    expect(describeReason(null)).toBe("no reason recorded");
  });
  it("tolerates missing or invalid timestamps", () => {
    expect(formatTime(undefined)).toBe("-");
    expect(formatTime("not a date")).toBe("-");
  });
});
