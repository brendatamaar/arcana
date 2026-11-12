import unittest
from words import count_words


class Hidden(unittest.TestCase):
    def test_apostrophes_stay_inside_words(self):
        self.assertEqual(count_words("Don't stop"), {"don't": 1, "stop": 1})

    def test_digits_and_case(self):
        self.assertEqual(count_words("a1 A1, b2"), {"a1": 2, "b2": 1})

    def test_empty_and_symbols_only(self):
        self.assertEqual(count_words(""), {})
        self.assertEqual(count_words("... --- !!!"), {})

    def test_hyphen_separates(self):
        self.assertEqual(count_words("well-known"), {"well": 1, "known": 1})
