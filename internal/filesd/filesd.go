// Package filesd reads, validates and writes Prometheus file_sd files.
package filesd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"go.yaml.in/yaml/v4"
)

// Group is one file_sd target group: a list of targets plus labels
// shared by every target of the group.
type Group struct {
	Targets []string          `yaml:"targets" json:"targets"`
	Labels  map[string]string `yaml:"labels"  json:"labels"`
}

// File is a parsed file_sd file: an ordered list of groups.
type File struct {
	Groups []Group `yaml:"-" json:"groups"`
	// Header holds the leading comment lines (including "#") of the
	// source document; they are preserved on Render.
	Header []string `yaml:"-" json:"-"`
}

// ParseFile decodes a file_sd document (JSON or YAML list of groups).
func ParseFile(data []byte) (*File, error) {
	var groups []Group
	if err := yaml.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("parse file_sd yaml: %w", err)
	}
	if groups == nil {
		groups = []Group{}
	}
	return &File{Groups: groups, Header: headerComments(data)}, nil
}

// headerComments extracts the leading full-line comments of a document.
func headerComments(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" && len(out) == 0:
			// skip leading blank lines
		case strings.HasPrefix(trimmed, "#"):
			out = append(out, trimmed)
		default:
			return out
		}
	}
	return out
}

// Render serializes the file as a YAML list of groups — the canonical
// file_sd format, human-readable and diff-friendly. The parsed header
// comment is written back on top.
func (f *File) Render() ([]byte, error) {
	if f.Groups == nil {
		f.Groups = []Group{}
	}
	var b strings.Builder
	for _, line := range f.Header {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f.Groups); err != nil {
		return nil, err
	}
	enc.Close()
	return []byte(b.String()), nil
}

// Validation error codes surfaced to the UI.
type ValidationError struct {
	GroupIndex int
	Field      string
	Message    string
}

func (e *ValidationError) Error() string {
	if e.GroupIndex >= 0 {
		return fmt.Sprintf("group %d: %s", e.GroupIndex+1, e.Message)
	}
	return e.Message
}

// Validate checks the file_sd schema constraints and special labels.
func (f *File) Validate() []*ValidationError {
	var errs []*ValidationError
	for i, g := range f.Groups {
		if len(g.Targets) == 0 {
			errs = append(errs, &ValidationError{GroupIndex: i, Field: "targets", Message: "at least one non-empty target is required"})
			continue
		}
		for _, t := range g.Targets {
			if strings.TrimSpace(t) == "" {
				errs = append(errs, &ValidationError{GroupIndex: i, Field: "targets", Message: "target must not be empty"})
			}
		}
		errs = append(errs, validateLabels(i, g.Labels)...)
	}
	return errs
}

var (
	schemeRe      = regexp.MustCompile(`^(http|https)$`)
	metricsPathRe = regexp.MustCompile(`^/`)
	paramNameRe   = regexp.MustCompile(`^__param_(.+)$`)
)

func validateLabels(groupIndex int, labels map[string]string) []*ValidationError {
	var errs []*ValidationError
	var interval, timeout string
	for _, k := range sortedKeys(labels) {
		v := labels[k]
		switch {
		case k == "__scheme__":
			if !schemeRe.MatchString(v) {
				errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: k, Message: fmt.Sprintf("%q is not a valid scheme (http or https)", v)})
			}
		case k == "__metrics_path__":
			if !metricsPathRe.MatchString(v) {
				errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: k, Message: fmt.Sprintf("%q must begin with /", v)})
			}
		case k == "__scrape_interval__":
			interval = v
			if _, err := ParseDuration(v); err != nil {
				errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: k, Message: fmt.Sprintf("%q is not a valid duration", v)})
			}
		case k == "__scrape_timeout__":
			timeout = v
			if _, err := ParseDuration(v); err != nil {
				errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: k, Message: fmt.Sprintf("%q is not a valid duration", v)})
			}
		case strings.HasPrefix(k, "__param_"):
			// any __param_<name> label; the name must be non-empty
			if m := paramNameRe.FindStringSubmatch(k); m == nil || m[1] == "" {
				errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: k, Message: "param name must not be empty"})
			}
		}
	}
	if interval != "" && timeout != "" {
		di, erri := ParseDuration(interval)
		dt, errt := ParseDuration(timeout)
		if erri == nil && errt == nil && dt > di {
			errs = append(errs, &ValidationError{GroupIndex: groupIndex, Field: "__scrape_timeout__", Message: fmt.Sprintf("timeout %s must not exceed interval %s", timeout, interval)})
		}
	}
	return errs
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseDuration parses a Prometheus duration like "30s", "5m", "1h30m".
// Prometheus durations are composed of units ms, s, m, h, d, w, y and
// do not allow fractional values.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if s == "0" {
		return 0, nil
	}
	unitMap := map[string]time.Duration{
		"ms": time.Millisecond,
		"s":  time.Second,
		"m":  time.Minute,
		"h":  time.Hour,
		"d":  24 * time.Hour,
		"w":  7 * 24 * time.Hour,
		"y":  365 * 24 * time.Hour,
	}
	neg := false
	rest := s
	if strings.HasPrefix(rest, "-") {
		neg = true
		rest = rest[1:]
	}
	var total time.Duration
	for rest != "" {
		numEnd := 0
		for numEnd < len(rest) && rest[numEnd] >= '0' && rest[numEnd] <= '9' {
			numEnd++
		}
		if numEnd == 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		var num int64
		if _, err := fmt.Sscanf(rest[:numEnd], "%d", &num); err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		matched := false
		for _, u := range []string{"ms", "s", "m", "h", "d", "w", "y"} {
			if strings.HasPrefix(rest[numEnd:], u) {
				total += time.Duration(num) * unitMap[u]
				rest = rest[numEnd+len(u):]
				matched = true
				break
			}
		}
		if !matched {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
	}
	if neg {
		total = -total
	}
	return total, nil
}

// File names are <job>_<id>.yml; id may only use [a-zA-Z0-9._-].
var idRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidID reports whether an id is acceptable for a new file name.
func ValidID(id string) bool { return idRe.MatchString(id) }

// FileName builds the on-disk file name for job and id.
func FileName(job, id string) string { return job + "_" + id + ".yml" }

// Dir wraps the files_sd directory on disk. All file operations go
// through an os.Root, which confines name resolution to the directory
// (openat2 RESOLVE_BENEATH): both ".." traversal and symlink escapes
// are rejected at the system level, independently of ValidName.
type Dir struct {
	Path string
	root *os.Root
}

// NewDir opens the files_sd directory. The root is held for the process
// lifetime and never closed; if the directory is removed and recreated
// later, operations keep addressing the old inode.
func NewDir(path string) (*Dir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Dir{Path: path, root: root}, nil
}

// List returns the .yml/.yaml/.json files of the directory (backups with
// their .bkp_* suffix are skipped), sorted by name.
func (d *Dir) List() ([]string, error) {
	entries, err := fs.ReadDir(d.root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".json") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Read loads and parses one file_sd file by name.
func (d *Dir) Read(name string) (*File, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid file name %q", name)
	}
	data, err := d.root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return ParseFile(data)
}

// ValidName guards against path traversal: a plain base name with a
// known extension and no backup suffix.
func ValidName(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return false
	}
	if strings.Contains(name, ".bkp_") {
		return false
	}
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".json")
}

// tmpSeq numbers temp file names; together with the nanosecond time it
// keeps them unique across concurrent writers.
var tmpSeq atomic.Int64

// WriteFile atomically writes data under name: a temp file in the same
// directory followed by rename. The file mode is 0644 so Prometheus
// (possibly running as another user) can read it.
func (d *Dir) WriteFile(name string, data []byte) error {
	tmpName := fmt.Sprintf(".promui-%d-%d", time.Now().UnixNano(), tmpSeq.Add(1))
	tmp, err := d.root.Create(tmpName)
	if err != nil {
		return err
	}
	defer d.root.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// os.Create uses 0666&umask; make the file group/world-readable.
	if err := d.root.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return d.root.Rename(tmpName, name)
}

// BackupName returns the backup file name for name at time t:
// <name>.bkp_<YYYYMMDD_HHMMSS>_<9-digit zero-padded sequence>.
func BackupName(name string, t time.Time, seq uint32) string {
	return fmt.Sprintf("%s.bkp_%s_%09d", name, t.Format("20060102_150405"), seq)
}

// Backup copies name to a sibling backup file. Consecutive backups never
// overwrite each other; the returned name sorts chronologically.
func (d *Dir) Backup(name string) (string, error) {
	src, err := d.root.ReadFile(name)
	if err != nil {
		return "", err
	}
	now := time.Now()
	// nanosecond part, zero-padded to 9 digits for fixed width and sorting;
	// on a rare collision the sequence is bumped until the name is free
	seq := uint32(now.Nanosecond())
	bkp := BackupName(name, now.Truncate(time.Second), seq)
	for {
		if _, err := d.root.Stat(bkp); os.IsNotExist(err) {
			break
		}
		seq++
		bkp = BackupName(name, now.Truncate(time.Second), seq)
	}
	if err := d.root.WriteFile(bkp, src, 0o644); err != nil {
		return "", err
	}
	return bkp, nil
}

// Create writes a brand new, previously absent file.
func (d *Dir) Create(job, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid id %q: allowed characters are a-z A-Z 0-9 . _ -", id)
	}
	name := FileName(job, id)
	if !ValidName(name) {
		return fmt.Errorf("invalid file name %q", name)
	}
	if _, err := d.root.Stat(name); err == nil {
		return fmt.Errorf("file %q already exists", name)
	}
	f := &File{Groups: []Group{}}
	data, err := f.Render()
	if err != nil {
		return err
	}
	return d.WriteFile(name, data)
}

// Remove deletes one file by name; the root confines it to the
// files_sd directory.
func (d *Dir) Remove(name string) error {
	return d.root.Remove(name)
}
