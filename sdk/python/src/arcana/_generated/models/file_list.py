from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.file_entry import FileEntry


T = TypeVar("T", bound="FileList")


@_attrs_define
class FileList:
    """
    Attributes:
        items (list[FileEntry]):
    """

    items: list[FileEntry]

    def to_dict(self) -> dict[str, Any]:
        items = []
        for items_item_data in self.items:
            items_item = items_item_data.to_dict()
            items.append(items_item)

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "items": items,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.file_entry import FileEntry

        d = dict(src_dict)
        items = []
        _items = d.pop("items")
        for items_item_data in _items:
            items_item = FileEntry.from_dict(items_item_data)

            items.append(items_item)

        file_list = cls(
            items=items,
        )

        return file_list
