from __future__ import annotations

import datetime
from collections.abc import Mapping
from typing import Any, Self, TypeVar, cast

from attrs import define as _attrs_define

from ..types import UNSET, Unset

T = TypeVar("T", bound="ExecRecord")


@_attrs_define
class ExecRecord:
    """Metadata of one exec. Output is never stored; see the exec output stream.

    Attributes:
        id (str):
        sandbox_id (str):
        cwd (str):
        tty (bool):
        started_at (datetime.datetime):
        cancelled (bool):
        cmd (list[str] | None | Unset): argv when the exec was given as an array.
        shell (None | str | Unset): The shell string when the exec used the shell field.
        ended_at (datetime.datetime | None | Unset):
        exit_code (int | None | Unset): The command's real exit status; null while running or when it could not be
            confirmed.
        timed_out (bool | None | Unset):
        truncated (bool | None | Unset):
        stdout_bytes (int | None | Unset):
        stderr_bytes (int | None | Unset):
    """

    id: str
    sandbox_id: str
    cwd: str
    tty: bool
    started_at: datetime.datetime
    cancelled: bool
    cmd: list[str] | None | Unset = UNSET
    shell: None | str | Unset = UNSET
    ended_at: datetime.datetime | None | Unset = UNSET
    exit_code: int | None | Unset = UNSET
    timed_out: bool | None | Unset = UNSET
    truncated: bool | None | Unset = UNSET
    stdout_bytes: int | None | Unset = UNSET
    stderr_bytes: int | None | Unset = UNSET

    def to_dict(self) -> dict[str, Any]:
        id = self.id

        sandbox_id = self.sandbox_id

        cwd = self.cwd

        tty = self.tty

        started_at = self.started_at.isoformat()

        cancelled = self.cancelled

        cmd: list[str] | None | Unset
        if isinstance(self.cmd, Unset):
            cmd = UNSET
        elif isinstance(self.cmd, list):
            cmd = self.cmd

        else:
            cmd = self.cmd

        shell: None | str | Unset
        if isinstance(self.shell, Unset):
            shell = UNSET
        else:
            shell = self.shell

        ended_at: None | str | Unset
        if isinstance(self.ended_at, Unset):
            ended_at = UNSET
        elif isinstance(self.ended_at, datetime.datetime):
            ended_at = self.ended_at.isoformat()
        else:
            ended_at = self.ended_at

        exit_code: int | None | Unset
        if isinstance(self.exit_code, Unset):
            exit_code = UNSET
        else:
            exit_code = self.exit_code

        timed_out: bool | None | Unset
        if isinstance(self.timed_out, Unset):
            timed_out = UNSET
        else:
            timed_out = self.timed_out

        truncated: bool | None | Unset
        if isinstance(self.truncated, Unset):
            truncated = UNSET
        else:
            truncated = self.truncated

        stdout_bytes: int | None | Unset
        if isinstance(self.stdout_bytes, Unset):
            stdout_bytes = UNSET
        else:
            stdout_bytes = self.stdout_bytes

        stderr_bytes: int | None | Unset
        if isinstance(self.stderr_bytes, Unset):
            stderr_bytes = UNSET
        else:
            stderr_bytes = self.stderr_bytes

        field_dict: dict[str, Any] = {}

        field_dict.update(
            {
                "id": id,
                "sandbox_id": sandbox_id,
                "cwd": cwd,
                "tty": tty,
                "started_at": started_at,
                "cancelled": cancelled,
            }
        )
        if cmd is not UNSET:
            field_dict["cmd"] = cmd
        if shell is not UNSET:
            field_dict["shell"] = shell
        if ended_at is not UNSET:
            field_dict["ended_at"] = ended_at
        if exit_code is not UNSET:
            field_dict["exit_code"] = exit_code
        if timed_out is not UNSET:
            field_dict["timed_out"] = timed_out
        if truncated is not UNSET:
            field_dict["truncated"] = truncated
        if stdout_bytes is not UNSET:
            field_dict["stdout_bytes"] = stdout_bytes
        if stderr_bytes is not UNSET:
            field_dict["stderr_bytes"] = stderr_bytes

        return field_dict

    @classmethod
    def from_dict(cls, src_dict: Mapping[str, Any]) -> Self:
        d = dict(src_dict)
        id = d.pop("id")

        sandbox_id = d.pop("sandbox_id")

        cwd = d.pop("cwd")

        tty = d.pop("tty")

        started_at = datetime.datetime.fromisoformat(d.pop("started_at"))

        cancelled = d.pop("cancelled")

        def _parse_cmd(data: object) -> list[str] | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                cmd_type_0 = cast(list[str], data)

                return cmd_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[str] | None | Unset, data)

        cmd = _parse_cmd(d.pop("cmd", UNSET))

        def _parse_shell(data: object) -> None | str | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(None | str | Unset, data)

        shell = _parse_shell(d.pop("shell", UNSET))

        def _parse_ended_at(data: object) -> datetime.datetime | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, str):
                    raise TypeError()
                ended_at_type_0 = datetime.datetime.fromisoformat(data)

                return ended_at_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(datetime.datetime | None | Unset, data)

        ended_at = _parse_ended_at(d.pop("ended_at", UNSET))

        def _parse_exit_code(data: object) -> int | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(int | None | Unset, data)

        exit_code = _parse_exit_code(d.pop("exit_code", UNSET))

        def _parse_timed_out(data: object) -> bool | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(bool | None | Unset, data)

        timed_out = _parse_timed_out(d.pop("timed_out", UNSET))

        def _parse_truncated(data: object) -> bool | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(bool | None | Unset, data)

        truncated = _parse_truncated(d.pop("truncated", UNSET))

        def _parse_stdout_bytes(data: object) -> int | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(int | None | Unset, data)

        stdout_bytes = _parse_stdout_bytes(d.pop("stdout_bytes", UNSET))

        def _parse_stderr_bytes(data: object) -> int | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            return cast(int | None | Unset, data)

        stderr_bytes = _parse_stderr_bytes(d.pop("stderr_bytes", UNSET))

        exec_record = cls(
            id=id,
            sandbox_id=sandbox_id,
            cwd=cwd,
            tty=tty,
            started_at=started_at,
            cancelled=cancelled,
            cmd=cmd,
            shell=shell,
            ended_at=ended_at,
            exit_code=exit_code,
            timed_out=timed_out,
            truncated=truncated,
            stdout_bytes=stdout_bytes,
            stderr_bytes=stderr_bytes,
        )

        return exec_record
