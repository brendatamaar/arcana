from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar, cast

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.network import Network
    from ..models.resources import Resources


T = TypeVar("T", bound="Snapshot")


@_attrs_define
class Snapshot:
    """Compressed copy of a sandbox's /workspace plus its creation spec. Environment values are never stored.

    Attributes:
        id (str):
        source_sandbox_id (str):
        template (str):
        resources (Resources):
        network (Network):
        env_keys (list[str]):
        size_bytes (int):
        sha256 (str):
        created_at (datetime.datetime):
    """

    id: str
    source_sandbox_id: str
    template: str
    resources: Resources
    network: Network
    env_keys: list[str]
    size_bytes: int
    sha256: str
    created_at: datetime.datetime

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        source_sandbox_id = self.source_sandbox_id

        template = self.template

        resources = self.resources.to_dict()

        network = self.network.to_dict()

        env_keys = self.env_keys

        size_bytes = self.size_bytes

        sha256 = self.sha256

        created_at = self.created_at.isoformat()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "source_sandbox_id": source_sandbox_id,
                "template": template,
                "resources": resources,
                "network": network,
                "env_keys": env_keys,
                "size_bytes": size_bytes,
                "sha256": sha256,
                "created_at": created_at,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.network import Network
        from ..models.resources import Resources

        d = dict(src_dict)
        id = d.pop("id")

        source_sandbox_id = d.pop("source_sandbox_id")

        template = d.pop("template")

        resources = Resources.from_dict(d.pop("resources"))

        network = Network.from_dict(d.pop("network"))

        env_keys = cast(list[str], d.pop("env_keys"))

        size_bytes = d.pop("size_bytes")

        sha256 = d.pop("sha256")

        created_at = datetime.datetime.fromisoformat(d.pop("created_at"))

        snapshot = cls(
            id=id,
            source_sandbox_id=source_sandbox_id,
            template=template,
            resources=resources,
            network=network,
            env_keys=env_keys,
            size_bytes=size_bytes,
            sha256=sha256,
            created_at=created_at,
        )

        return snapshot
