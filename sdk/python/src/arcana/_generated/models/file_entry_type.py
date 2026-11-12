from enum import StrEnum


class FileEntryType(StrEnum):
    DIRECTORY = "directory"
    FILE = "file"
    OTHER = "other"
    SYMLINK = "symlink"

    def __str__(self) -> str:
        return str(self.value)
