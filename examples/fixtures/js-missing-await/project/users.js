async function getUserName(repo, id) {
  const user = repo.find(id);
  return user.name;
}

module.exports = { getUserName };
