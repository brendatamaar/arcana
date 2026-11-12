from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Self, TypeVar

from attrs import define as _attrs_define

if TYPE_CHECKING:
    from ..models.pool_fill import PoolFill
    from ..models.template_count import TemplateCount
    from ..models.termination import Termination


T = TypeVar("T", bound="Overview")


@_attrs_define
class Overview:
    """Admin keys see installation-wide numbers; other keys see their own, and no pool fill.

    Attributes:
        version (str):
        runtime (str):
        sandboxes_by_template (list[TemplateCount]):
        pool (list[PoolFill]):
        recent_terminations (list[Termination]):
    """

    version: str
    runtime: str
    sandboxes_by_template: list[TemplateCount]
    pool: list[PoolFill]
    recent_terminations: list[Termination]

    def to_dict(self) -> dict[str, Any]:
        version = self.version

        runtime = self.runtime

        sandboxes_by_template = []
        for sandboxes_by_template_item_data in self.sandboxes_by_template:
            sandboxes_by_template_item = sandboxes_by_template_item_data.to_dict()
            sandboxes_by_template.append(sandboxes_by_template_item)

        pool = []
        for pool_item_data in self.pool:
            pool_item = pool_item_data.to_dict()
            pool.append(pool_item)

        recent_terminations = []
        for recent_terminations_item_data in self.recent_terminations:
            recent_terminations_item = recent_terminations_item_data.to_dict()
            recent_terminations.append(recent_terminations_item)

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "version": version,
                "runtime": runtime,
                "sandboxes_by_template": sandboxes_by_template,
                "pool": pool,
                "recent_terminations": recent_terminations,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        from ..models.pool_fill import PoolFill
        from ..models.template_count import TemplateCount
        from ..models.termination import Termination

        d = dict(src_dict)
        version = d.pop("version")

        runtime = d.pop("runtime")

        sandboxes_by_template = []
        _sandboxes_by_template = d.pop("sandboxes_by_template")
        for sandboxes_by_template_item_data in _sandboxes_by_template:
            sandboxes_by_template_item = TemplateCount.from_dict(
                sandboxes_by_template_item_data
            )

            sandboxes_by_template.append(sandboxes_by_template_item)

        pool = []
        _pool = d.pop("pool")
        for pool_item_data in _pool:
            pool_item = PoolFill.from_dict(pool_item_data)

            pool.append(pool_item)

        recent_terminations = []
        _recent_terminations = d.pop("recent_terminations")
        for recent_terminations_item_data in _recent_terminations:
            recent_terminations_item = Termination.from_dict(
                recent_terminations_item_data
            )

            recent_terminations.append(recent_terminations_item)

        overview = cls(
            version=version,
            runtime=runtime,
            sandboxes_by_template=sandboxes_by_template,
            pool=pool,
            recent_terminations=recent_terminations,
        )

        return overview
