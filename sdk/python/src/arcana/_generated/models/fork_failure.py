from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="ForkFailure")


@_attrs_define
class ForkFailure:
    """
    Attributes:
        index (int): Zero-based position of the failed sandbox in the request.
        code (str):
        message (str):
    """

    index: int
    code: str
    message: str

    def to_dict(self) -> dict[str, Any]:
        index = self.index

        code = self.code

        message = self.message

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "index": index,
                "code": code,
                "message": message,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        index = d.pop("index")

        code = d.pop("code")

        message = d.pop("message")

        fork_failure = cls(
            index=index,
            code=code,
            message=message,
        )

        return fork_failure
