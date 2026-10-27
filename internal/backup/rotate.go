package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"taskronin/internal/fsutil"
)

const (
	// MinKeepDaily is the minimum number of UTC calendar days retained.
	MinKeepDaily = 3
	// MinKeepWeekly is the minimum number of ISO calendar weeks retained.
	MinKeepWeekly = 1
)

// Retention specifies the number of recent calendar days and ISO weeks to keep.
type Retention struct {
	Daily, Weekly int
}

var snapshotPattern = regexp.MustCompile(`^taskronin-([0-9]{8}T[0-9]{6}\.[0-9]{3}Z)-[0-9a-fA-F]{4}-(daily|manual|premigrate|prescrub)\.db$`)

// ParseSnapshotName parses a snapshot basename, rejecting foreign files and
// invalid dates. Path, Size and SchemaVersion are left at their zero values.
func ParseSnapshotName(name string) (SnapshotInfo, bool) {
	parts := snapshotPattern.FindStringSubmatch(name)
	if parts == nil {
		return SnapshotInfo{}, false
	}
	at, err := time.Parse(snapshotTimeLayout, parts[1])
	if err != nil {
		return SnapshotInfo{}, false
	}
	return SnapshotInfo{Name: name, Kind: Kind(parts[2]), At: at}, true
}

// List returns regular snapshot files newest first, using only names and file
// metadata. It never opens databases; SchemaVersion remains zero.
func List(dir string) ([]SnapshotInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("backup: list snapshots: %w", err)
	}
	var infos []SnapshotInfo
	for _, entry := range entries {
		info, ok := ParseSnapshotName(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		stat, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("backup: stat listed snapshot: %w", err)
		}
		if !stat.Mode().IsRegular() {
			continue
		}
		info.Path, info.Size = filepath.Join(dir, info.Name), stat.Size()
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b SnapshotInfo) int {
		if order := b.At.Compare(a.At); order != 0 {
			return order
		}
		return strings.Compare(b.Name, a.Name)
	})
	return infos, nil
}

// Rotate deletes snapshots outside r and returns their basenames, newest first.
// It always keeps snapshots younger than 24 hours and the newest three overall.
// Foreign files, directories, and symlinks are untouched.
func (m *Manager) Rotate(ctx context.Context, r Retention) (removed []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Daily < MinKeepDaily || r.Weekly < MinKeepWeekly {
		return nil, fmt.Errorf("backup: retention must keep at least %d daily and %d weekly", MinKeepDaily, MinKeepWeekly)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	infos, err := List(m.Dir)
	if err != nil {
		return nil, err
	}
	now := m.Clock.Now().UTC()
	today := utcDay(now)
	week := isoWeekStart(today)
	days, weeks := make(map[time.Time]bool), make(map[time.Time]bool)
	defer func() {
		if len(removed) != 0 {
			if syncErr := fsutil.FsyncDir(m.Dir); syncErr != nil {
				err = errors.Join(err, fmt.Errorf("backup: sync rotated snapshots: %w", syncErr))
			}
		}
	}()
	for i, info := range infos {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		day := utcDay(info.At)
		dayAge := (today.Unix() - day.Unix()) / (24 * 60 * 60)
		snapshotWeek := isoWeekStart(day)
		weekAge := (week.Unix() - snapshotWeek.Unix()) / (7 * 24 * 60 * 60)
		keep := i < 3 || now.Sub(info.At) < 24*time.Hour
		if dayAge >= 0 && dayAge < int64(r.Daily) && !days[day] {
			keep = true
		}
		if weekAge >= 0 && weekAge < int64(r.Weekly) && !weeks[snapshotWeek] {
			keep = true
		}
		days[day], weeks[snapshotWeek] = true, true
		if keep {
			continue
		}
		if err := os.Remove(info.Path); err != nil {
			return removed, fmt.Errorf("backup: remove snapshot %s: %w", info.Name, err)
		}
		removed = append(removed, info.Name)
	}
	return removed, nil
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func isoWeekStart(day time.Time) time.Time {
	// AIDEV-NOTE: ISO weeks start Monday. Monday dates remain distinct across
	// ISO-year boundaries, including years containing week 53.
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}
