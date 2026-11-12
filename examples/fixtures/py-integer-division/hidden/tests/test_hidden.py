import unittest
from stats import mean


class Hidden(unittest.TestCase):
    def test_repeating_fraction(self):
        self.assertAlmostEqual(mean([1, 2, 4]), 7 / 3)

    def test_negative_values(self):
        self.assertEqual(mean([-1, -2]), -1.5)

    def test_empty_raises(self):
        with self.assertRaises(ValueError):
            mean([])
