from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

from ..models.error_error_code import ErrorErrorCode

T = TypeVar("T", bound="ErrorError")


@_attrs_define
class ErrorError:
    """
    Attributes:
        code (ErrorErrorCode):
        message (str):
        request_id (str):
    """

    code: ErrorErrorCode
    message: str
    request_id: str

    def to_dict(self) -> dict[str, Any]:
        code = self.code.value

        message = self.message

        request_id = self.request_id

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "code": code,
                "message": message,
                "request_id": request_id,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        code = ErrorErrorCode(d.pop("code"))

        message = d.pop("message")

        request_id = d.pop("request_id")

        error_error = cls(
            code=code,
            message=message,
            request_id=request_id,
        )

        return error_error
