from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..types import UNSET, Unset

if TYPE_CHECKING:
    from ..models.create_sandbox_env import CreateSandboxEnv
    from ..models.create_sandbox_labels import CreateSandboxLabels
    from ..models.network import Network
    from ..models.resources import Resources


T = TypeVar("T", bound="CreateSandbox")


@_attrs_define
class CreateSandbox:
    """
    Attributes:
        template (str):
        env (CreateSandboxEnv | Unset):
        from_snapshot (None | str | Unset): Snapshot to restore into /workspace. The template must match the snapshot's
            template; resources and network default to the snapshot's recorded values.
        idle_timeout_seconds (int | Unset):
        labels (CreateSandboxLabels | Unset):
        network (Network | Unset):
        resources (Resources | Unset):
        ttl_seconds (int | Unset):
    """

    template: str
    env: CreateSandboxEnv | Unset = UNSET
    from_snapshot: None | str | Unset = UNSET
    idle_timeout_seconds: int | Unset = UNSET
    labels: CreateSandboxLabels | Unset = UNSET
    network: Network | Unset = UNSET
    resources: Resources | Unset = UNSET
    ttl_seconds: int | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        template = self.template

        env: dict[str, Any] | Unset = UNSET
        if not isinstance(self.env, Unset):
            env = self.env.to_dict()

        from_snapshot: None | str | Unset
        if isinstance(self.from_snapshot, Unset):
            from_snapshot = UNSET
        else:
            from_snapshot = self.from_snapshot

        idle_timeout_seconds = self.idle_timeout_seconds

        labels: dict[str, Any] | Unset = UNSET
        if not isinstance(self.labels, Unset):
            labels = self.labels.to_dict()

        network: dict[str, Any] | Unset = UNSET
        if not isinstance(self.network, Unset):
            network = self.network.to_dict()

        resources: dict[str, Any] | Unset = UNSET
        if not isinstance(self.resources, Unset):
            resources = self.resources.to_dict()

        ttl_seconds = self.ttl_seconds

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "template": template,
            }
        )
        if env is not UNSET:
            field_dict["env"] = env
        if from_snapshot is not UNSET:
            field_dict["from_snapshot"] = from_snapshot
        if idle_timeout_seconds is not UNSET:
            field_dict["idle_timeout_seconds"] = idle_timeout_seconds
        if labels is not UNSET:
            field_dict["labels"] = labels
        if network is not UNSET:
            field_dict["network"] = network
        if resources is not UNSET:
            field_dict["resources"] = resources
        if ttl_seconds is not UNSET:
            field_dict["ttl_seconds"] = ttl_seconds

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.create_sandbox_env import CreateSandboxEnv
        from ..models.create_sandbox_labels import CreateSandboxLabels
        from ..models.network import Network
        from ..models.resources import Resources

        d = dict(src_dict)
        template = d.pop("template")

        _env = d.pop("env", UNSET)
        env: CreateSandboxEnv | Unset
        if isinstance(_env, Unset):
            env = UNSET
        else:
            env = CreateSandboxEnv.from_dict(_env)

        def _parse_from_snapshot(data: object) -> None | str | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(None | str | Unset, data)

        from_snapshot = _parse_from_snapshot(d.pop("from_snapshot", UNSET))

        idle_timeout_seconds = d.pop("idle_timeout_seconds", UNSET)

        _labels = d.pop("labels", UNSET)
        labels: CreateSandboxLabels | Unset
        if isinstance(_labels, Unset):
            labels = UNSET
        else:
            labels = CreateSandboxLabels.from_dict(_labels)

        _network = d.pop("network", UNSET)
        network: Network | Unset
        if isinstance(_network, Unset):
            network = UNSET
        else:
            network = Network.from_dict(_network)

        _resources = d.pop("resources", UNSET)
        resources: Resources | Unset
        if isinstance(_resources, Unset):
            resources = UNSET
        else:
            resources = Resources.from_dict(_resources)

        ttl_seconds = d.pop("ttl_seconds", UNSET)

        create_sandbox = cls(
            template=template,
            env=env,
            from_snapshot=from_snapshot,
            idle_timeout_seconds=idle_timeout_seconds,
            labels=labels,
            network=network,
            resources=resources,
            ttl_seconds=ttl_seconds,
        )

        return create_sandbox
