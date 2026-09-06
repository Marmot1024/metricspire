"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {resolvePresetRange, zonedMidnightISO} = require("./app.js");

test("zoned midnight preserves the business timezone across DST", () => {
  assert.equal(zonedMidnightISO(2026, 9, 1, "Asia/Shanghai"), "2026-08-31T16:00:00.000Z");
  assert.equal(zonedMidnightISO(2026, 3, 8, "America/Los_Angeles"), "2026-03-08T08:00:00.000Z");
  assert.equal(zonedMidnightISO(2026, 3, 9, "America/Los_Angeles"), "2026-03-09T07:00:00.000Z");
});

test("relative presets become explicit half-open absolute ranges", () => {
  const now = new Date("2026-09-05T02:00:00Z");
  assert.deepEqual(resolvePresetRange("yesterday", "Asia/Shanghai", now), {
    start: "2026-09-03T16:00:00.000Z",
    end: "2026-09-04T16:00:00.000Z",
  });
  assert.deepEqual(resolvePresetRange("previous_month", "Asia/Shanghai", now), {
    start: "2026-07-31T16:00:00.000Z",
    end: "2026-08-31T16:00:00.000Z",
  });
  assert.deepEqual(resolvePresetRange("custom", "Asia/Shanghai", now, {start: "2026-08-01", end: "2026-09-01"}), {
    start: "2026-07-31T16:00:00.000Z",
    end: "2026-08-31T16:00:00.000Z",
  });
});
