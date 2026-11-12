const test = require("node:test");
const assert = require("node:assert/strict");
const { getCity } = require("../city");

test("reads the city", () => {
  assert.equal(getCity({ address: { city: "Oslo" } }), "Oslo");
});

test("missing address gives undefined", () => {
  assert.equal(getCity({ name: "x" }), undefined);
});
