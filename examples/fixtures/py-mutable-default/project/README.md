# cart

`add_item(item, items=None)` appends `item` to `items` **in place** and returns that list. When `items` is
omitted, a brand new list is used for each call, so separate calls never share state.
