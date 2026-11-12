import unittest
from sumrange import sum_range


class Visible(unittest.TestCase):
    def test_inclusive(self):
        self.assertEqual(sum_range(1, 5), 15)
