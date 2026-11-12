const test = require("node:test");
const assert = require("node:assert/strict");
const { sortNumbers } = require("../sort");

test("handles negatives and floats", () => {
  assert.deepEqual(sortNumbers([2.5, -3, 10, 2, -20]), [-20, -3, 2, 2.5, 10]);
});

test("does not mutate its input", () => {
  const input = [3, 1, 2];
  sortNumbers(input);
  assert.deepEqual(input, [3, 1, 2]);
});

test("empty input", () => {
  assert.deepEqual(sortNumbers([]), []);
});
