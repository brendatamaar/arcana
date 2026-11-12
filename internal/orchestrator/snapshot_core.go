package orchestrator

import "regexp"

// snapshotLimits are the operator bounds that decide whether another snapshot may start.
type snapshotLimits struct {
	maxPerKey     int
	maxTotalBytes int64
	maxArchive    int64
}

// snapshotUsage counts stored plus in-flight snapshots so concurrent creations cannot overshoot.
type snapshotUsage struct {
	ownerCount int
	totalBytes int64
}

// snapshotBudget decides admission as a pure function. It returns the byte cap for the new
// archive and whether that cap comes from the global store budget rather than the per-archive limit.
func snapshotBudget(l snapshotLimits, u snapshotUsage) (limit int64, global bool, err error) {
	if u.ownerCount >= l.maxPerKey {
		return 0, false, ErrQuota
	}
	remaining := l.maxTotalBytes - u.totalBytes
	if remaining <= 0 {
		return 0, true, ErrQuota
	}
	if remaining < l.maxArchive {
		return remaining, true, nil
	}
	return l.maxArchive, false, nil
}

// snapshotFile matches the only names Arcana writes under data_dir/snapshots.
var snapshotFile = regexp.MustCompile(`^(snp_[a-z2-7]{20})\.tar\.gz(\.part)?$`)

// snapshotMember decides whether a directory entry is a finished archive, an interrupted
// write or foreign. Foreign names are never touched.
func snapshotMember(name string) (id string, partial, ours bool) {
	m := snapshotFile.FindStringSubmatch(name)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] != "", true
}
