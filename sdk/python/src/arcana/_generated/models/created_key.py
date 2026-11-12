from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.key import Key


T = TypeVar("T", bound="CreatedKey")


@_attrs_define
class CreatedKey:
    """
    Attributes:
        key (Key):
        secret (str):
    """

    key: Key
    secret: str

    def to_dict(self) -> dict[str, Any]:
        key = self.key.to_dict()

        secret = self.secret

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "key": key,
                "secret": secret,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.key import Key

        d = dict(src_dict)
        key = Key.from_dict(d.pop("key"))

        secret = d.pop("secret")

        created_key = cls(
            key=key,
            secret=secret,
        )

        return created_key
