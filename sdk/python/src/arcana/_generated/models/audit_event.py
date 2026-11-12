from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.audit_event_details import AuditEventDetails


T = TypeVar("T", bound="AuditEvent")


@_attrs_define
class AuditEvent:
    """
    Attributes:
        id (str):
        ts (datetime.datetime):
        key_id (str):
        sandbox_id (str):
        action (str):
        details (AuditEventDetails):
    """

    id: str
    ts: datetime.datetime
    key_id: str
    sandbox_id: str
    action: str
    details: AuditEventDetails

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        ts = self.ts.isoformat()

        key_id = self.key_id

        sandbox_id = self.sandbox_id

        action = self.action

        details = self.details.to_dict()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "ts": ts,
                "key_id": key_id,
                "sandbox_id": sandbox_id,
                "action": action,
                "details": details,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.audit_event_details import AuditEventDetails

        d = dict(src_dict)
        id = d.pop("id")

        ts = datetime.datetime.fromisoformat(d.pop("ts"))

        key_id = d.pop("key_id")

        sandbox_id = d.pop("sandbox_id")

        action = d.pop("action")

        details = AuditEventDetails.from_dict(d.pop("details"))

        audit_event = cls(
            id=id,
            ts=ts,
            key_id=key_id,
            sandbox_id=sandbox_id,
            action=action,
            details=details,
        )

        return audit_event
