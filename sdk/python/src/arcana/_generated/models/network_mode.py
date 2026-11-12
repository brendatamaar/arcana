from enum import StrEnum


class NetworkMode(StrEnum):
    ALLOWLIST = "allowlist"
    NONE = "none"

    def __str__(self) -> str:
        return str(self.value)
