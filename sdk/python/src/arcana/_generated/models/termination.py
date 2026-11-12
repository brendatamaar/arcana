from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..models.sandbox_state import SandboxState
from ..types import UNSET, Unset

T = TypeVar("T", bound="Termination")


@_attrs_define
class Termination:
    """
    Attributes:
        id (str):
        template (str):
        state (SandboxState):
        ended_at (datetime.datetime):
        reason (None | str | Unset):
    """

    id: str
    template: str
    state: SandboxState
    ended_at: datetime.datetime
    reason: None | str | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        template = self.template

        state = self.state.value

        ended_at = self.ended_at.isoformat()

        reason: None | str | Unset
        if isinstance(self.reason, Unset):
            reason = UNSET
        else:
            reason = self.reason

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "template": template,
                "state": state,
                "ended_at": ended_at,
            }
        )
        if reason is not UNSET:
            field_dict["reason"] = reason

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        id = d.pop("id")

        template = d.pop("template")

        state = SandboxState(d.pop("state"))

        ended_at = datetime.datetime.fromisoformat(d.pop("ended_at"))

        def _parse_reason(data: object) -> None | str | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(None | str | Unset, data)

        reason = _parse_reason(d.pop("reason", UNSET))

        termination = cls(
            id=id,
            template=template,
            state=state,
            ended_at=ended_at,
            reason=reason,
        )

        return termination
