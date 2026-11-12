from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

from ..types import UNSET, Unset

T = TypeVar("T", bound="CreateKey")


@_attrs_define
class CreateKey:
    """
    Attributes:
        name (str):
        is_admin (bool | Unset):
    """

    name: str
    is_admin: bool | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        name = self.name

        is_admin = self.is_admin

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "name": name,
            }
        )
        if is_admin is not UNSET:
            field_dict["is_admin"] = is_admin

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        name = d.pop("name")

        is_admin = d.pop("is_admin", UNSET)

        create_key = cls(
            name=name,
            is_admin=is_admin,
        )

        return create_key
