from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..models.sandbox_state import SandboxState
from ..types import UNSET, Unset

T = TypeVar("T", bound="StateChange")


@_attrs_define
class StateChange:
    """
    Attributes:
        state (SandboxState):
        at (datetime.datetime):
        reason (None | str | Unset):
    """

    state: SandboxState
    at: datetime.datetime
    reason: None | str | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        state = self.state.value

        at = self.at.isoformat()

        reason: None | str | Unset
        if isinstance(self.reason, Unset):
            reason = UNSET
        else:
            reason = self.reason

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "state": state,
                "at": at,
            }
        )
        if reason is not UNSET:
            field_dict["reason"] = reason

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        state = SandboxState(d.pop("state"))

        at = datetime.datetime.fromisoformat(d.pop("at"))

        def _parse_reason(data: object) -> None | str | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(None | str | Unset, data)

        reason = _parse_reason(d.pop("reason", UNSET))

        state_change = cls(
            state=state,
            at=at,
            reason=reason,
        )

        return state_change
