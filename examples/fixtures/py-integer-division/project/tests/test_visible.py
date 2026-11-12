import unittest
from stats import mean


class Visible(unittest.TestCase):
    def test_fractional_mean(self):
        self.assertEqual(mean([1, 2]), 1.5)
