import unittest
from words import count_words


class Visible(unittest.TestCase):
    def test_punctuation_is_not_part_of_words(self):
        self.assertEqual(count_words("The cat. the dog!"), {"the": 2, "cat": 1, "dog": 1})
