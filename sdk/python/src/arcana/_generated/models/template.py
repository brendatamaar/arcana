from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..types import UNSET, Unset

if TYPE_CHECKING:
    from ..models.resources import Resources


T = TypeVar("T", bound="Template")


@_attrs_define
class Template:
    """
    Attributes:
        name (str):
        image (str):
        resources (Resources):
        ttl_seconds (int):
        idle_timeout_seconds (int):
        workdir (str):
        network_mode (str):
        warm_pool (int):
        env_keys (list[str]):
        network_preset (str | Unset):
    """

    name: str
    image: str
    resources: Resources
    ttl_seconds: int
    idle_timeout_seconds: int
    workdir: str
    network_mode: str
    warm_pool: int
    env_keys: list[str]
    network_preset: str | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        name = self.name

        image = self.image

        resources = self.resources.to_dict()

        ttl_seconds = self.ttl_seconds

        idle_timeout_seconds = self.idle_timeout_seconds

        workdir = self.workdir

        network_mode = self.network_mode

        warm_pool = self.warm_pool

        env_keys = self.env_keys

        network_preset = self.network_preset

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "name": name,
                "image": image,
                "resources": resources,
                "ttl_seconds": ttl_seconds,
                "idle_timeout_seconds": idle_timeout_seconds,
                "workdir": workdir,
                "network_mode": network_mode,
                "warm_pool": warm_pool,
                "env_keys": env_keys,
            }
        )
        if network_preset is not UNSET:
            field_dict["network_preset"] = network_preset

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.resources import Resources

        d = dict(src_dict)
        name = d.pop("name")

        image = d.pop("image")

        resources = Resources.from_dict(d.pop("resources"))

        ttl_seconds = d.pop("ttl_seconds")

        idle_timeout_seconds = d.pop("idle_timeout_seconds")

        workdir = d.pop("workdir")

        network_mode = d.pop("network_mode")

        warm_pool = d.pop("warm_pool")

        env_keys = cast(list[str], d.pop("env_keys"))

        network_preset = d.pop("network_preset", UNSET)

        template = cls(
            name=name,
            image=image,
            resources=resources,
            ttl_seconds=ttl_seconds,
            idle_timeout_seconds=idle_timeout_seconds,
            workdir=workdir,
            network_mode=network_mode,
            warm_pool=warm_pool,
            env_keys=env_keys,
            network_preset=network_preset,
        )

        return template
