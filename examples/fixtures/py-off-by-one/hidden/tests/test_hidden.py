import unittest
from sumrange import sum_range


class Hidden(unittest.TestCase):
    def test_single_value(self):
        self.assertEqual(sum_range(3, 3), 3)

    def test_negative_span(self):
        self.assertEqual(sum_range(-2, 2), 0)

    def test_empty_when_reversed(self):
        self.assertEqual(sum_range(5, 4), 0)
