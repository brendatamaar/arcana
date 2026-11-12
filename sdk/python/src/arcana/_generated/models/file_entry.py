from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

from ..models.file_entry_type import FileEntryType
from ..types import UNSET, Unset

T = TypeVar("T", bound="FileEntry")


@_attrs_define
class FileEntry:
    """
    Attributes:
        name (str):
        path (str):
        size_bytes (int):
        type_ (FileEntryType):
        mode (int | Unset):
        modified_at (datetime.datetime | Unset):
    """

    name: str
    path: str
    size_bytes: int
    type_: FileEntryType
    mode: int | Unset = UNSET
    modified_at: datetime.datetime | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        name = self.name

        path = self.path

        size_bytes = self.size_bytes

        type_ = self.type_.value

        mode = self.mode

        modified_at: str | Unset = UNSET
        if not isinstance(self.modified_at, Unset):
            modified_at = self.modified_at.isoformat()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "name": name,
                "path": path,
                "size_bytes": size_bytes,
                "type": type_,
            }
        )
        if mode is not UNSET:
            field_dict["mode"] = mode
        if modified_at is not UNSET:
            field_dict["modified_at"] = modified_at

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        name = d.pop("name")

        path = d.pop("path")

        size_bytes = d.pop("size_bytes")

        type_ = FileEntryType(d.pop("type"))

        mode = d.pop("mode", UNSET)

        _modified_at = d.pop("modified_at", UNSET)
        modified_at: datetime.datetime | Unset
        if isinstance(_modified_at, Unset):
            modified_at = UNSET
        else:
            modified_at = datetime.datetime.fromisoformat(_modified_at)

        file_entry = cls(
            name=name,
            path=path,
            size_bytes=size_bytes,
            type_=type_,
            mode=mode,
            modified_at=modified_at,
        )

        return file_entry
