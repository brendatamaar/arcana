import re


def count_words(text):
    counts = {}
    for word in re.findall(r"[a-z0-9']+", text.lower()):
        counts[word] = counts.get(word, 0) + 1
    return counts
