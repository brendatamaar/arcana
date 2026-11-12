# users

`getUserName(repo, id)` resolves to the name of the user `repo.find(id)` returns. `repo.find` is
asynchronous and resolves to a user object or `null`. For an unknown id, `getUserName` rejects with
`Error("user not found: <id>")`.
