def mean(values):
    if len(values) == 0:
        raise ValueError("mean of an empty sequence")
    return sum(values) / len(values)
