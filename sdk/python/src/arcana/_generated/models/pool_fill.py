from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="PoolFill")


@_attrs_define
class PoolFill:
    """
    Attributes:
        template (str):
        available (int):
        target (int):
    """

    template: str
    available: int
    target: int

    def to_dict(self) -> dict[str, Any]:
        template = self.template

        available = self.available

        target = self.target

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "template": template,
                "available": available,
                "target": target,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        template = d.pop("template")

        available = d.pop("available")

        target = d.pop("target")

        pool_fill = cls(
            template=template,
            available=available,
            target=target,
        )

        return pool_fill
