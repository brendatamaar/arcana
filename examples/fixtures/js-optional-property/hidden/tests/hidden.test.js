const test = require("node:test");
const assert = require("node:assert/strict");
const { getCity } = require("../city");

test("null and undefined users", () => {
  assert.equal(getCity(null), undefined);
  assert.equal(getCity(undefined), undefined);
});

test("address without a city and null address", () => {
  assert.equal(getCity({ address: {} }), undefined);
  assert.equal(getCity({ address: null }), undefined);
});

test("falsy but present city is returned", () => {
  assert.equal(getCity({ address: { city: "" } }), "");
});
