from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Self, TypeVar

from attrs import define as _attrs_define

T = TypeVar("T", bound="ExecResult")


@_attrs_define
class ExecResult:
    """
    Attributes:
        duration_ms (int):
        exec_id (str):
        exit_code (int):
        stderr (str):
        stdout (str):
        timed_out (bool):
        truncated (bool):
    """

    duration_ms: int
    exec_id: str
    exit_code: int
    stderr: str
    stdout: str
    timed_out: bool
    truncated: bool

    def to_dict(self) -> dict[str, Any]:
        duration_ms = self.duration_ms

        exec_id = self.exec_id

        exit_code = self.exit_code

        stderr = self.stderr

        stdout = self.stdout

        timed_out = self.timed_out

        truncated = self.truncated

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "duration_ms": duration_ms,
                "exec_id": exec_id,
                "exit_code": exit_code,
                "stderr": stderr,
                "stdout": stdout,
                "timed_out": timed_out,
                "truncated": truncated,
            }
        )

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        duration_ms = d.pop("duration_ms")

        exec_id = d.pop("exec_id")

        exit_code = d.pop("exit_code")

        stderr = d.pop("stderr")

        stdout = d.pop("stdout")

        timed_out = d.pop("timed_out")

        truncated = d.pop("truncated")

        exec_result = cls(
            duration_ms=duration_ms,
            exec_id=exec_id,
            exit_code=exit_code,
            stderr=stderr,
            stdout=stdout,
            timed_out=timed_out,
            truncated=truncated,
        )

        return exec_result
