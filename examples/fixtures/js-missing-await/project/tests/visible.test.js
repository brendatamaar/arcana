const test = require("node:test");
const assert = require("node:assert/strict");
const { getUserName } = require("../users");

const repo = { find: async (id) => (id === 1 ? { name: "ada" } : null) };

test("resolves the user's name", async () => {
  assert.equal(await getUserName(repo, 1), "ada");
});
