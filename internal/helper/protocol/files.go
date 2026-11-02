package protocol

import "time"

// File-operation exit codes are interpreted only for internal helper utilities.
const (
	FileNotFound   = 10
	FileDenied     = 11
	FileInvalid    = 12
	FileTooLarge   = 13
	FileNoSpace    = 14
	MaxListEntries = 1000
	MaxListBytes   = 8 * 1048576
)

// FileHeader describes the opened regular file before its opaque byte stream.
type FileHeader struct {
	SizeBytes int64 `json:"size_bytes"`
}

// FileEntry is bounded metadata produced inside the sandbox; names remain hostile text.
type FileEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Type       string    `json:"type"`
	SizeBytes  int64     `json:"size_bytes"`
	Mode       int64     `json:"mode"`
	ModifiedAt time.Time `json:"modified_at"`
}
