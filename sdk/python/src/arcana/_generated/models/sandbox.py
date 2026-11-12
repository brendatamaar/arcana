from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..models.sandbox_source import SandboxSource
from ..models.sandbox_state import SandboxState
from ..types import UNSET, Unset

if TYPE_CHECKING:
    from ..models.network import Network
    from ..models.resources import Resources
    from ..models.sandbox_labels import SandboxLabels


T = TypeVar("T", bound="Sandbox")


@_attrs_define
class Sandbox:
    """
    Attributes:
        created_at (datetime.datetime):
        expires_at (datetime.datetime):
        id (str):
        labels (SandboxLabels):
        network (Network):
        resources (Resources):
        runtime (str):
        source (SandboxSource):
        state (SandboxState):
        template (str):
        end_reason (None | str | Unset):
        ended_at (datetime.datetime | None | Unset):
        idle_timeout_seconds (int | Unset):
        last_activity_at (datetime.datetime | Unset):
    """

    created_at: datetime.datetime
    expires_at: datetime.datetime
    id: str
    labels: SandboxLabels
    network: Network
    resources: Resources
    runtime: str
    source: SandboxSource
    state: SandboxState
    template: str
    end_reason: None | str | Unset = UNSET
    ended_at: datetime.datetime | None | Unset = UNSET
    idle_timeout_seconds: int | Unset = UNSET
    last_activity_at: datetime.datetime | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        created_at = self.created_at.isoformat()

        expires_at = self.expires_at.isoformat()

        id = self.id

        labels = self.labels.to_dict()

        network = self.network.to_dict()

        resources = self.resources.to_dict()

        runtime = self.runtime

        source = self.source.value

        state = self.state.value

        template = self.template

        end_reason: None | str | Unset
        if isinstance(self.end_reason, Unset):
            end_reason = UNSET
        else:
            end_reason = self.end_reason

        ended_at: None | str | Unset
        if isinstance(self.ended_at, Unset):
            ended_at = UNSET
        elif isinstance(self.ended_at, datetime.datetime):
            ended_at = self.ended_at.isoformat()
        else:
            ended_at = self.ended_at

        idle_timeout_seconds = self.idle_timeout_seconds

        last_activity_at: str | Unset = UNSET
        if not isinstance(self.last_activity_at, Unset):
            last_activity_at = self.last_activity_at.isoformat()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "created_at": created_at,
                "expires_at": expires_at,
                "id": id,
                "labels": labels,
                "network": network,
                "resources": resources,
                "runtime": runtime,
                "source": source,
                "state": state,
                "template": template,
            }
        )
        if end_reason is not UNSET:
            field_dict["end_reason"] = end_reason
        if ended_at is not UNSET:
            field_dict["ended_at"] = ended_at
        if idle_timeout_seconds is not UNSET:
            field_dict["idle_timeout_seconds"] = idle_timeout_seconds
        if last_activity_at is not UNSET:
            field_dict["last_activity_at"] = last_activity_at

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.network import Network
        from ..models.resources import Resources
        from ..models.sandbox_labels import SandboxLabels

        d = dict(src_dict)
        created_at = datetime.datetime.fromisoformat(d.pop("created_at"))

        expires_at = datetime.datetime.fromisoformat(d.pop("expires_at"))

        id = d.pop("id")

        labels = SandboxLabels.from_dict(d.pop("labels"))

        network = Network.from_dict(d.pop("network"))

        resources = Resources.from_dict(d.pop("resources"))

        runtime = d.pop("runtime")

        source = SandboxSource(d.pop("source"))

        state = SandboxState(d.pop("state"))

        template = d.pop("template")

        def _parse_end_reason(data: object) -> None | str | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(None | str | Unset, data)

        end_reason = _parse_end_reason(d.pop("end_reason", UNSET))

        def _parse_ended_at(data: object) -> datetime.datetime | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, str):
                    raise TypeError()
                ended_at_type_0 = datetime.datetime.fromisoformat(data)

                return ended_at_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(datetime.datetime | None | Unset, data)

        ended_at = _parse_ended_at(d.pop("ended_at", UNSET))

        idle_timeout_seconds = d.pop("idle_timeout_seconds", UNSET)

        _last_activity_at = d.pop("last_activity_at", UNSET)
        last_activity_at: datetime.datetime | Unset
        if isinstance(_last_activity_at, Unset):
            last_activity_at = UNSET
        else:
            last_activity_at = datetime.datetime.fromisoformat(_last_activity_at)

        sandbox = cls(
            created_at=created_at,
            expires_at=expires_at,
            id=id,
            labels=labels,
            network=network,
            resources=resources,
            runtime=runtime,
            source=source,
            state=state,
            template=template,
            end_reason=end_reason,
            ended_at=ended_at,
            idle_timeout_seconds=idle_timeout_seconds,
            last_activity_at=last_activity_at,
        )

        return sandbox
