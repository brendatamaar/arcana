import unittest
from cart import add_item


class Visible(unittest.TestCase):
    def test_calls_are_independent(self):
        self.assertEqual(add_item("a"), ["a"])
        self.assertEqual(add_item("b"), ["b"])
