package selfsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T, installed bool) (self, target string) {
	t.Helper()
	dir := t.TempDir()
	self = filepath.Join(dir, "bundle", "mind-runner")
	target = filepath.Join(dir, "bin", "mind-runner")
	if err := os.MkdirAll(filepath.Dir(self), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if installed {
		if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return self, target
}

func ver(out string) VersionFunc {
	return func(context.Context, string) (string, error) { return out, nil }
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSyncCopiesWhenOlder(t *testing.T) {
	self, target := setup(t, true)
	r, err := Sync(context.Background(), self, target, "0.1.4", ver("mind-runner 0.1.3 (abc)\n"))
	if err != nil || !r.Copied || r.Installed != "0.1.3" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if content(t, target) != "NEW" {
		t.Fatal("target chưa được thay")
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode=%v", st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".mind-runner.selfsync-*"))
	if len(left) != 0 {
		t.Fatalf("còn file tạm: %v", left)
	}
}

func TestSyncSkips(t *testing.T) {
	cases := []struct {
		name, cur, out string
		installed      bool
		verErr         bool
	}{
		{"cur dev", "dev", "mind-runner 0.1.0", true, false},
		{"not installed", "0.1.4", "", false, false},
		{"installed dev", "0.1.4", "mind-runner dev", true, false},
		{"same version", "0.1.4", "mind-runner 0.1.4", true, false},
		{"installed newer", "0.1.4", "mind-runner 0.2.0", true, false},
		{"minor compare numeric", "0.1.9", "mind-runner 0.1.10", true, false},
		{"version fails", "0.1.4", "", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			self, target := setup(t, c.installed)
			vf := ver(c.out)
			if c.verErr {
				vf = func(context.Context, string) (string, error) { return "", errors.New("boom") }
			}
			r, err := Sync(context.Background(), self, target, c.cur, vf)
			if err != nil || r.Copied || r.Reason == "" {
				t.Fatalf("r=%+v err=%v", r, err)
			}
			if c.installed && content(t, target) != "OLD" {
				t.Fatal("target bị ghi đè")
			}
			if !c.installed {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("không được tự tạo bản cài")
				}
			}
		})
	}
}

func TestSyncSameFile(t *testing.T) {
	_, target := setup(t, true)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r, err := Sync(context.Background(), link, target, "0.1.4", ver("mind-runner 0.1.0"))
	if err != nil || r.Copied {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := parseVersion("v1.2.3-rc1"); !ok || v != [3]int{1, 2, 3} {
		t.Fatal(v, ok)
	}
	for _, s := range []string{"dev", "", "1.2", "1.x.3"} {
		if _, ok := parseVersion(s); ok {
			t.Fatal(s)
		}
	}
}
