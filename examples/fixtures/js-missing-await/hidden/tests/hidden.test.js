const test = require("node:test");
const assert = require("node:assert/strict");
const { getUserName } = require("../users");

const repo = { find: async (id) => (id === 1 ? { name: "ada" } : null) };

test("unknown ids reject with a clear error", async () => {
  await assert.rejects(getUserName(repo, 99), { message: "user not found: 99" });
});

test("works with a repo that resolves later", async () => {
  const slow = { find: (id) => new Promise((resolve) => setTimeout(() => resolve({ name: `u${id}` }), 5)) };
  assert.equal(await getUserName(slow, 7), "u7");
});
