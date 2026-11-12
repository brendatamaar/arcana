from enum import StrEnum


class SandboxState(StrEnum):
    CREATING = "creating"
    DESTROYED = "destroyed"
    DESTROYING = "destroying"
    EXPIRED = "expired"
    FAILED = "failed"
    RUNNING = "running"

    def __str__(self) -> str:
        return str(self.value)
