from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.state_change import StateChange


T = TypeVar("T", bound="StateHistory")


@_attrs_define
class StateHistory:
    """A sandbox's persisted state transitions in order, with the reason where one was recorded.

    Attributes:
        items (list[StateChange]):
    """

    items: list[StateChange]

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
        from ..models.state_change import StateChange

        d = dict(src_dict)
        items = []
        _items = d.pop("items")
        for items_item_data in _items:
            items_item = StateChange.from_dict(items_item_data)

            items.append(items_item)

        state_history = cls(
            items=items,
        )

        return state_history
