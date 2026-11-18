package filesd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoundtrip(t *testing.T) {
	src := `[{"targets":["h1:9100","h2:9100"],"labels":{"env":"prod"}}]`
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Groups) != 1 || len(f.Groups[0].Targets) != 2 || f.Groups[0].Labels["env"] != "prod" {
		t.Fatalf("unexpected groups: %+v", f.Groups)
	}
	out, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	f2, err := ParseFile(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(f2.Groups) != 1 || f2.Groups[0].Targets[0] != "h1:9100" {
		t.Fatalf("roundtrip mismatch: %+v", f2.Groups)
	}
}

func TestParseYAMLAndInvalid(t *testing.T) {
	if _, err := ParseFile([]byte("- targets: [a:1]\n  labels:\n    x: y\n")); err != nil {
		t.Fatalf("yaml list should parse: %v", err)
	}
	if _, err := ParseFile([]byte(":::not a list")); err == nil {
		t.Fatal("broken yaml should fail")
	}
}

func TestRenderYAMLFormatAndHeader(t *testing.T) {
	src := "# node_exporter targets (job: node_exporter).\n" +
		"# Managed in promui under node_exporter/.\n" +
		"- targets:\n    - h1:9100\n  labels:\n    env: demo\n"
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	// Output must stay YAML, not flip to JSON.
	for _, want := range []string{
		"# node_exporter targets (job: node_exporter).",
		"# Managed in promui under node_exporter/.",
		"- targets:",
		"    - h1:9100",
		"    env: demo",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("render output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "{") || strings.Contains(s, `"targets"`) {
		t.Fatalf("render output looks like JSON, want YAML:\n%s", s)
	}
	// Roundtrip must keep the header too.
	f2, err := ParseFile(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(f2.Header) != 2 {
		t.Fatalf("header lost on roundtrip: %+v", f2.Header)
	}
}

func TestRenderEmptyFile(t *testing.T) {
	f := &File{} // e.g. a freshly created file with no groups yet
	out, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if string(out) != "[]\n" {
		t.Fatalf("empty file should render as an empty YAML list, got %q", out)
	}
}

func TestValidateSchema(t *testing.T) {
	f := &File{Groups: []Group{
		{Targets: nil, Labels: map[string]string{}}, // no targets
		{Targets: []string{"  "}, Labels: nil},      // blank target
	}}
	errs := f.Validate()
	if len(errs) != 2 {
		t.Fatalf("want 2 errors, got %v", errs)
	}
}

func TestValidateSpecialLabels(t *testing.T) {
	good := &File{Groups: []Group{{
		Targets: []string{"a:1"},
		Labels: map[string]string{
			"__scheme__":          "https",
			"__metrics_path__":    "/custom",
			"__scrape_interval__": "30s",
			"__scrape_timeout__":  "10s",
			"__param_match":       "[]()",
		},
	}}}
	if errs := good.Validate(); len(errs) > 0 {
		t.Fatalf("valid labels rejected: %v", errs)
	}

	bad := &File{Groups: []Group{{
		Targets: []string{"a:1"},
		Labels: map[string]string{
			"__scheme__":          "ftp",
			"__metrics_path__":    "no-slash",
			"__scrape_interval__": "10s",
			"__scrape_timeout__":  "1m",
			"__param_":            "x",
		},
	}}}
	errs := bad.Validate()
	if len(errs) != 4 {
		t.Fatalf("want 4 errors, got %d: %v", len(errs), errs)
	}
	var sawScheme, sawPath, sawTimeout, sawParam bool
	for _, e := range errs {
		switch e.Field {
		case "__scheme__":
			sawScheme = true
		case "__metrics_path__":
			sawPath = true
		case "__scrape_timeout__":
			sawTimeout = true
		case "__param_":
			sawParam = true
		}
	}
	if !sawScheme || !sawPath || !sawTimeout || !sawParam {
		t.Fatalf("missing expected error fields: %v", errs)
	}
}

func TestValidateUnknownDunderLabels(t *testing.T) {
	f := &File{Groups: []Group{{Targets: []string{"a:1"}, Labels: map[string]string{"__weird__": "x"}}}}
	if errs := f.Validate(); len(errs) != 0 {
		t.Fatalf("unknown __ labels must pass: %v", errs)
	}
}

func TestParseDuration(t *testing.T) {
	ok := map[string]bool{
		"30s": true, "5m": true, "1h30m": true, "0": true, "1y": true, "2w": true, "100ms": true, "1d": true,
		"": false, "s": false, "1.5s": false, "5x": false, "m5": false, "-3s": true, "1h1m1s1ms": true,
	}
	for in, want := range ok {
		_, err := ParseDuration(in)
		if (err == nil) != want {
			t.Errorf("ParseDuration(%q) error = %v, want ok=%v", in, err, want)
		}
	}
}

func TestValidIDAndFileName(t *testing.T) {
	for _, id := range []string{"a1", "prod-2", "x.y", "A_b"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "../etc", "a/b", "a b", "a|b"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
	if FileName("job", "a1") != "job_a1.yml" {
		t.Fatal("FileName mismatch")
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"job_a1.yml", "a.yaml", "a.json"} {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false", n)
		}
	}
	for _, n := range []string{"", "/etc/passwd", "../x.yml", "a/b.yml", "x.yml.bkp_20260101_000000_000000001", "x.txt"} {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true", n)
		}
	}
}

func newDir(t *testing.T) *Dir {
	t.Helper()
	return &Dir{Path: t.TempDir()}
}

func TestCreateAndCollision(t *testing.T) {
	d := newDir(t)
	if err := d.Create("myjob", "a1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := d.Create("myjob", "a1"); err == nil {
		t.Fatal("collision must fail")
	}
	if err := d.Create("myjob", "a/b"); err == nil {
		t.Fatal("slash in id must fail")
	}
	f, err := d.Read("myjob_a1.yml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(f.Groups) != 0 {
		t.Fatalf("new file must be empty, got %+v", f.Groups)
	}
}

func TestWriteAtomic(t *testing.T) {
	d := newDir(t)
	if err := d.WriteFile("j_a.yml", []byte("[]")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(d.Path, "j_a.yml"))
	if err != nil || string(data) != "[]" {
		t.Fatalf("write/read mismatch: %q %v", data, err)
	}
	// no temp files left behind
	entries, _ := os.ReadDir(d.Path)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".promui-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestBackupFormatAndUniqueness(t *testing.T) {
	d := newDir(t)
	if err := d.WriteFile("j_a.yml", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	b1, err := d.Backup("j_a.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b1, "j_a.yml.bkp_") {
		t.Fatalf("backup name: %q", b1)
	}
	// fixed width ns suffix: exactly 9 digits after the second underscore
	parts := strings.Split(b1, "_")
	ns := parts[len(parts)-1]
	if len(ns) != 9 || strings.TrimLeft(ns, "0") == "" && ns != "000000000" {
		t.Fatalf("ns part %q is not 9 digits", ns)
	}
	for _, r := range ns {
		if r < '0' || r > '9' {
			t.Fatalf("ns part %q is not numeric", ns)
		}
	}
	// backup must not match *.yml glob
	if ok, _ := filepath.Match("*.yml", b1); ok {
		t.Fatalf("backup %q would be picked up by *.yml glob", b1)
	}
	// second backup right after: different name, sorts later
	if err := d.WriteFile("j_a.yml", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	b2, err := d.Backup("j_a.yml")
	if err != nil {
		t.Fatal(err)
	}
	if b1 == b2 {
		t.Fatalf("backups collide: %q", b1)
	}
	if !(b1 < b2 || strings.HasPrefix(b2, b1[:len(b1)-9])) {
		// names sort chronologically within the same second
		if b2 < b1 && !strings.HasPrefix(b1, b2[:len(b2)-9]) {
			t.Fatalf("backup names do not sort chronologically: %q vs %q", b1, b2)
		}
	}
	// both backups exist with original content
	for i, b := range []string{b1, b2} {
		data, err := os.ReadFile(filepath.Join(d.Path, b))
		if err != nil {
			t.Fatal(err)
		}
		want := []byte("v1")
		if i == 1 {
			want = []byte("v2")
		}
		if string(data) != string(want) {
			t.Fatalf("backup %s content = %q, want %q", b, data, want)
		}
	}
}

func TestBackupNameFormat(t *testing.T) {
	ts := time.Date(2026, 11, 17, 15, 30, 5, 42, time.UTC)
	got := BackupName("myjob_a1.yml", ts, 42)
	want := "myjob_a1.yml.bkp_20261117_153005_000000042"
	if got != want {
		t.Fatalf("BackupName = %q, want %q", got, want)
	}
}

func TestListSkipsBackups(t *testing.T) {
	d := newDir(t)
	_ = d.WriteFile("a.yml", []byte("[]"))
	_ = d.WriteFile("a.yml.bkp_20260101_000000_000000001", []byte("[]"))
	_ = d.WriteFile("b.txt", []byte("x"))
	names, err := d.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "a.yml" {
		t.Fatalf("List = %v", names)
	}
}
