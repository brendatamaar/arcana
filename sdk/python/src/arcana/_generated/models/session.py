from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="Session")


@_attrs_define
class Session:
    """A dashboard browser session. Its token is delivered only as an HttpOnly cookie.

    Attributes:
        id (str):
        expires_at (datetime.datetime):
    """

    id: str
    expires_at: datetime.datetime

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        expires_at = self.expires_at.isoformat()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "expires_at": expires_at,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        id = d.pop("id")

        expires_at = datetime.datetime.fromisoformat(d.pop("expires_at"))

        session = cls(
            id=id,
            expires_at=expires_at,
        )

        return session
