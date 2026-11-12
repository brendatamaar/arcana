from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.fork_failure import ForkFailure
    from ..models.sandbox import Sandbox


T = TypeVar("T", bound="ForkResult")


@_attrs_define
class ForkResult:
    """
    Attributes:
        items (list[Sandbox]):
        failures (list[ForkFailure]):
    """

    items: list[Sandbox]
    failures: list[ForkFailure]

    def to_dict(self) -> dict[str, Any]:
        items = []
        for items_item_data in self.items:
            items_item = items_item_data.to_dict()
            items.append(items_item)

        failures = []
        for failures_item_data in self.failures:
            failures_item = failures_item_data.to_dict()
            failures.append(failures_item)

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "items": items,
                "failures": failures,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.fork_failure import ForkFailure
        from ..models.sandbox import Sandbox

        d = dict(src_dict)
        items = []
        _items = d.pop("items")
        for items_item_data in _items:
            items_item = Sandbox.from_dict(items_item_data)

            items.append(items_item)

        failures = []
        _failures = d.pop("failures")
        for failures_item_data in _failures:
            failures_item = ForkFailure.from_dict(failures_item_data)

            failures.append(failures_item)

        fork_result = cls(
            items=items,
            failures=failures,
        )

        return fork_result
