import unittest
from cart import add_item


class Hidden(unittest.TestCase):
    def test_explicit_list_is_mutated_in_place(self):
        mine = ["x"]
        result = add_item("y", mine)
        self.assertIs(result, mine)
        self.assertEqual(mine, ["x", "y"])

    def test_fresh_lists_are_distinct_objects(self):
        self.assertIsNot(add_item(1), add_item(1))

    def test_duplicates_are_kept(self):
        self.assertEqual(add_item("a", ["a"]), ["a", "a"])
