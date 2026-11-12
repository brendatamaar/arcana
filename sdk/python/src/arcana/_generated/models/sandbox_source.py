from enum import StrEnum


class SandboxSource(StrEnum):
    COLD = "cold"
    POOL = "pool"
    SNAPSHOT = "snapshot"

    def __str__(self) -> str:
        return str(self.value)
