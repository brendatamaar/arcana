from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

from ..types import UNSET, Unset

if TYPE_CHECKING:
    from ..models.fork_request_env import ForkRequestEnv
    from ..models.fork_request_labels import ForkRequestLabels
    from ..models.network import Network
    from ..models.resources import Resources


T = TypeVar("T", bound="ForkRequest")


@_attrs_define
class ForkRequest:
    """Overrides apply to every sandbox created by the fork.

    Attributes:
        count (int):
        ttl_seconds (int | Unset):
        idle_timeout_seconds (int | Unset):
        resources (Resources | Unset):
        network (Network | Unset):
        env (ForkRequestEnv | Unset):
        labels (ForkRequestLabels | Unset):
    """

    count: int
    ttl_seconds: int | Unset = UNSET
    idle_timeout_seconds: int | Unset = UNSET
    resources: Resources | Unset = UNSET
    network: Network | Unset = UNSET
    env: ForkRequestEnv | Unset = UNSET
    labels: ForkRequestLabels | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        count = self.count

        ttl_seconds = self.ttl_seconds

        idle_timeout_seconds = self.idle_timeout_seconds

        resources: dict[str, Any] | Unset = UNSET
        if not isinstance(self.resources, Unset):
            resources = self.resources.to_dict()

        network: dict[str, Any] | Unset = UNSET
        if not isinstance(self.network, Unset):
            network = self.network.to_dict()

        env: dict[str, Any] | Unset = UNSET
        if not isinstance(self.env, Unset):
            env = self.env.to_dict()

        labels: dict[str, Any] | Unset = UNSET
        if not isinstance(self.labels, Unset):
            labels = self.labels.to_dict()

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "count": count,
            }
        )
        if ttl_seconds is not UNSET:
            field_dict["ttl_seconds"] = ttl_seconds
        if idle_timeout_seconds is not UNSET:
            field_dict["idle_timeout_seconds"] = idle_timeout_seconds
        if resources is not UNSET:
            field_dict["resources"] = resources
        if network is not UNSET:
            field_dict["network"] = network
        if env is not UNSET:
            field_dict["env"] = env
        if labels is not UNSET:
            field_dict["labels"] = labels

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.fork_request_env import ForkRequestEnv
        from ..models.fork_request_labels import ForkRequestLabels
        from ..models.network import Network
        from ..models.resources import Resources

        d = dict(src_dict)
        count = d.pop("count")

        ttl_seconds = d.pop("ttl_seconds", UNSET)

        idle_timeout_seconds = d.pop("idle_timeout_seconds", UNSET)

        _resources = d.pop("resources", UNSET)
        resources: Resources | Unset
        if isinstance(_resources, Unset):
            resources = UNSET
        else:
            resources = Resources.from_dict(_resources)

        _network = d.pop("network", UNSET)
        network: Network | Unset
        if isinstance(_network, Unset):
            network = UNSET
        else:
            network = Network.from_dict(_network)

        _env = d.pop("env", UNSET)
        env: ForkRequestEnv | Unset
        if isinstance(_env, Unset):
            env = UNSET
        else:
            env = ForkRequestEnv.from_dict(_env)

        _labels = d.pop("labels", UNSET)
        labels: ForkRequestLabels | Unset
        if isinstance(_labels, Unset):
            labels = UNSET
        else:
            labels = ForkRequestLabels.from_dict(_labels)

        fork_request = cls(
            count=count,
            ttl_seconds=ttl_seconds,
            idle_timeout_seconds=idle_timeout_seconds,
            resources=resources,
            network=network,
            env=env,
            labels=labels,
        )

        return fork_request
