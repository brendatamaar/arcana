async function getUserName(repo, id) {
  const user = await repo.find(id);
  if (!user) {
    throw new Error(`user not found: ${id}`);
  }
  return user.name;
}

module.exports = { getUserName };
