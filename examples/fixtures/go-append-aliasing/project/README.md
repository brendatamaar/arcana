# derive

`WithItem(items, item)` returns a new slice holding `items` followed by `item`. It never modifies
`items`, and results derived from the same input never affect each other, even when `items` has spare capacity.
