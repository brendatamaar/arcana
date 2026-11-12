from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..types import UNSET, Unset

T = TypeVar("T", bound="Key")


@_attrs_define
class Key:
    """
    Attributes:
        created_at (datetime.datetime):
        id (str):
        is_admin (bool):
        name (str):
        prefix (str):
        revoked_at (datetime.datetime | None | Unset):
    """

    created_at: datetime.datetime
    id: str
    is_admin: bool
    name: str
    prefix: str
    revoked_at: datetime.datetime | None | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        created_at = self.created_at.isoformat()

        id = self.id

        is_admin = self.is_admin

        name = self.name

        prefix = self.prefix

        revoked_at: None | str | Unset
        if isinstance(self.revoked_at, Unset):
            revoked_at = UNSET
        elif isinstance(self.revoked_at, datetime.datetime):
            revoked_at = self.revoked_at.isoformat()
        else:
            revoked_at = self.revoked_at

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "created_at": created_at,
                "id": id,
                "is_admin": is_admin,
                "name": name,
                "prefix": prefix,
            }
        )
        if revoked_at is not UNSET:
            field_dict["revoked_at"] = revoked_at

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        created_at = datetime.datetime.fromisoformat(d.pop("created_at"))

        id = d.pop("id")

        is_admin = d.pop("is_admin")

        name = d.pop("name")

        prefix = d.pop("prefix")

        def _parse_revoked_at(data: object) -> datetime.datetime | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, str):
                    raise TypeError()
                revoked_at_type_0 = datetime.datetime.fromisoformat(data)

                return revoked_at_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(datetime.datetime | None | Unset, data)

        revoked_at = _parse_revoked_at(d.pop("revoked_at", UNSET))

        key = cls(
            created_at=created_at,
            id=id,
            is_admin=is_admin,
            name=name,
            prefix=prefix,
            revoked_at=revoked_at,
        )

        return key
