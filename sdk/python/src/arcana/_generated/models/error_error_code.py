from enum import StrEnum


class ErrorErrorCode(StrEnum):
    EXEC_LIMIT_REACHED = "exec_limit_reached"
    FILE_NOT_FOUND = "file_not_found"
    FORBIDDEN = "forbidden"
    INTERNAL = "internal"
    INVALID_PATH = "invalid_path"
    INVALID_REQUEST = "invalid_request"
    INVALID_TEMPLATE = "invalid_template"
    KEY_NOT_FOUND = "key_not_found"
    PAYLOAD_TOO_LARGE = "payload_too_large"
    QUOTA_EXCEEDED = "quota_exceeded"
    RATE_LIMITED = "rate_limited"
    RUNTIME_UNAVAILABLE = "runtime_unavailable"
    SANDBOX_NOT_FOUND = "sandbox_not_found"
    SANDBOX_NOT_RUNNING = "sandbox_not_running"
    SHUTTING_DOWN = "shutting_down"
    SNAPSHOT_NOT_FOUND = "snapshot_not_found"
    UNAUTHORIZED = "unauthorized"

    def __str__(self) -> str:
        return str(self.value)
