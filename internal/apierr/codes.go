package apierr

const (
	// InvalidRequest is the invalid_request error code.
	InvalidRequest Code = "invalid_request"
	// InvalidPath is the invalid_path error code.
	InvalidPath Code = "invalid_path"
	// InvalidTemplate is the invalid_template error code.
	InvalidTemplate Code = "invalid_template"
	// Unauthorized is the unauthorized error code.
	Unauthorized Code = "unauthorized"
	// Forbidden is the forbidden error code.
	Forbidden Code = "forbidden"
	// SandboxNotFound is the sandbox_not_found error code.
	SandboxNotFound Code = "sandbox_not_found"
	// SnapshotNotFound is the snapshot_not_found error code.
	SnapshotNotFound Code = "snapshot_not_found"
	// FileNotFound is the file_not_found error code.
	FileNotFound Code = "file_not_found"
	// KeyNotFound is the key_not_found error code.
	KeyNotFound Code = "key_not_found"
	// SandboxNotRunning is the sandbox_not_running error code.
	SandboxNotRunning Code = "sandbox_not_running"
	// ExecLimitReached is the exec_limit_reached error code.
	ExecLimitReached Code = "exec_limit_reached"
	// PayloadTooLarge is the payload_too_large error code.
	PayloadTooLarge Code = "payload_too_large"
	// QuotaExceeded is the quota_exceeded error code.
	QuotaExceeded Code = "quota_exceeded"
	// RateLimited is the rate_limited error code.
	RateLimited Code = "rate_limited"
	// Internal is the internal error code.
	Internal Code = "internal"
	// RuntimeUnavailable is the runtime_unavailable error code.
	RuntimeUnavailable Code = "runtime_unavailable"
	// ShuttingDown is the shutting_down error code.
	ShuttingDown Code = "shutting_down"
)

// Status returns the HTTP status, defaulting unknown codes to 500.
func (e *Error) Status() int {
	switch e.Code {
	case InvalidRequest:
		return 400
	case InvalidPath:
		return 400
	case InvalidTemplate:
		return 400
	case Unauthorized:
		return 401
	case Forbidden:
		return 403
	case SandboxNotFound:
		return 404
	case SnapshotNotFound:
		return 404
	case FileNotFound:
		return 404
	case KeyNotFound:
		return 404
	case SandboxNotRunning:
		return 409
	case ExecLimitReached:
		return 409
	case PayloadTooLarge:
		return 413
	case QuotaExceeded:
		return 429
	case RateLimited:
		return 429
	case Internal:
		return 500
	case RuntimeUnavailable:
		return 503
	case ShuttingDown:
		return 503
	default:
		return 500
	}
}

// NewInvalidRequest constructs a invalid_request error.
func NewInvalidRequest(message string) *Error { return &Error{Code: InvalidRequest, Message: message} }

// NewInvalidPath constructs a invalid_path error.
func NewInvalidPath(message string) *Error { return &Error{Code: InvalidPath, Message: message} }

// NewInvalidTemplate constructs a invalid_template error.
func NewInvalidTemplate(message string) *Error {
	return &Error{Code: InvalidTemplate, Message: message}
}

// NewUnauthorized constructs a unauthorized error.
func NewUnauthorized(message string) *Error { return &Error{Code: Unauthorized, Message: message} }

// NewForbidden constructs a forbidden error.
func NewForbidden(message string) *Error { return &Error{Code: Forbidden, Message: message} }

// NewSandboxNotFound constructs a sandbox_not_found error.
func NewSandboxNotFound(message string) *Error {
	return &Error{Code: SandboxNotFound, Message: message}
}

// NewSnapshotNotFound constructs a snapshot_not_found error.
func NewSnapshotNotFound(message string) *Error {
	return &Error{Code: SnapshotNotFound, Message: message}
}

// NewFileNotFound constructs a file_not_found error.
func NewFileNotFound(message string) *Error { return &Error{Code: FileNotFound, Message: message} }

// NewKeyNotFound constructs a key_not_found error.
func NewKeyNotFound(message string) *Error { return &Error{Code: KeyNotFound, Message: message} }

// NewSandboxNotRunning constructs a sandbox_not_running error.
func NewSandboxNotRunning(message string) *Error {
	return &Error{Code: SandboxNotRunning, Message: message}
}

// NewExecLimitReached constructs a exec_limit_reached error.
func NewExecLimitReached(message string) *Error {
	return &Error{Code: ExecLimitReached, Message: message}
}

// NewPayloadTooLarge constructs a payload_too_large error.
func NewPayloadTooLarge(message string) *Error {
	return &Error{Code: PayloadTooLarge, Message: message}
}

// NewQuotaExceeded constructs a quota_exceeded error.
func NewQuotaExceeded(message string) *Error { return &Error{Code: QuotaExceeded, Message: message} }

// NewRateLimited constructs a rate_limited error.
func NewRateLimited(message string) *Error { return &Error{Code: RateLimited, Message: message} }

// NewInternal constructs a internal error.
func NewInternal(message string) *Error { return &Error{Code: Internal, Message: message} }

// NewRuntimeUnavailable constructs a runtime_unavailable error.
func NewRuntimeUnavailable(message string) *Error {
	return &Error{Code: RuntimeUnavailable, Message: message}
}

// NewShuttingDown constructs a shutting_down error.
func NewShuttingDown(message string) *Error { return &Error{Code: ShuttingDown, Message: message} }
