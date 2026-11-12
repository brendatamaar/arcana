const test = require("node:test");
const assert = require("node:assert/strict");
const { sortNumbers } = require("../sort");

test("sorts numerically", () => {
  assert.deepEqual(sortNumbers([10, 9, 1]), [1, 9, 10]);
});
