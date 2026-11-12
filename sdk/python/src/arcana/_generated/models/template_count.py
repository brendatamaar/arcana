from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="TemplateCount")


@_attrs_define
class TemplateCount:
    """
    Attributes:
        template (str):
        count (int):
    """

    template: str
    count: int

    def to_dict(self) -> dict[str, Any]:
        template = self.template

        count = self.count

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "template": template,
                "count": count,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        template = d.pop("template")

        count = d.pop("count")

        template_count = cls(
            template=template,
            count=count,
        )

        return template_count
