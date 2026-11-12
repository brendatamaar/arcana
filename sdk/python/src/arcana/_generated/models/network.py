from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..models.network_mode import NetworkMode
from ..types import UNSET, Unset

T = TypeVar("T", bound="Network")


@_attrs_define
class Network:
    """
    Attributes:
        mode (NetworkMode):
        allow (list[str] | Unset):
        presets (list[str] | Unset):
    """

    mode: NetworkMode
    allow: list[str] | Unset = UNSET
    presets: list[str] | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        mode = self.mode.value

        allow: list[str] | Unset = UNSET
        if not isinstance(self.allow, Unset):
            allow = self.allow

        presets: list[str] | Unset = UNSET
        if not isinstance(self.presets, Unset):
            presets = self.presets

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "mode": mode,
            }
        )
        if allow is not UNSET:
            field_dict["allow"] = allow
        if presets is not UNSET:
            field_dict["presets"] = presets

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        mode = NetworkMode(d.pop("mode"))

        allow = cast(list[str], d.pop("allow", UNSET))

        presets = cast(list[str], d.pop("presets", UNSET))

        network = cls(
            mode=mode,
            allow=allow,
            presets=presets,
        )

        return network
