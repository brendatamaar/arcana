from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

from ..types import UNSET, Unset

T = TypeVar("T", bound="Resources")


@_attrs_define
class Resources:
    """
    Attributes:
        cpus (float | Unset):
        disk_mb (int | Unset):
        memory_mb (int | Unset):
        pids (int | Unset):
    """

    cpus: float | Unset = UNSET
    disk_mb: int | Unset = UNSET
    memory_mb: int | Unset = UNSET
    pids: int | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        cpus = self.cpus

        disk_mb = self.disk_mb

        memory_mb = self.memory_mb

        pids = self.pids

        field_dict: dict[str, Any] = {}

        field_dict.update({})
        if cpus is not UNSET:
            field_dict["cpus"] = cpus
        if disk_mb is not UNSET:
            field_dict["disk_mb"] = disk_mb
        if memory_mb is not UNSET:
            field_dict["memory_mb"] = memory_mb
        if pids is not UNSET:
            field_dict["pids"] = pids

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        cpus = d.pop("cpus", UNSET)

        disk_mb = d.pop("disk_mb", UNSET)

        memory_mb = d.pop("memory_mb", UNSET)

        pids = d.pop("pids", UNSET)

        resources = cls(
            cpus=cpus,
            disk_mb=disk_mb,
            memory_mb=memory_mb,
            pids=pids,
        )

        return resources
