from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="Session")


@_attrs_define
class Session:
    """A dashboard browser session. Its token is delivered only as an HttpOnly cookie. The CSRF token is the synchronizer
    token that cookie-authenticated writes must send in the X-CSRF-Token header; it is useless without the cookie.

        Attributes:
            id (str):
            expires_at (datetime.datetime):
            csrf_token (str):
            admin (bool): Whether the session's key is an admin key, so the dashboard can show key management and the audit
                view.
    """

    id: str
    expires_at: datetime.datetime
    csrf_token: str
    admin: bool

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        expires_at = self.expires_at.isoformat()

        csrf_token = self.csrf_token

        admin = self.admin

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "expires_at": expires_at,
                "csrf_token": csrf_token,
                "admin": admin,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        id = d.pop("id")

        expires_at = datetime.datetime.fromisoformat(d.pop("expires_at"))

        csrf_token = d.pop("csrf_token")

        admin = d.pop("admin")

        session = cls(
            id=id,
            expires_at=expires_at,
            csrf_token=csrf_token,
            admin=admin,
        )

        return session
