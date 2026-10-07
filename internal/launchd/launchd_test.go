package launchd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil, nil
}

func TestInstallDarwin(t *testing.T) {
	old := goos
	goos = "darwin"
	t.Cleanup(func() { goos = old })

	home := t.TempDir()
	logDir := filepath.Join(home, "data", "logs")
	fr := &fakeRunner{}
	if err := Install(context.Background(), fr, "/bin/mind-runner", home, logDir); err != nil {
		t.Fatal(err)
	}

	plist := filepath.Join(home, "Library", "LaunchAgents", "com.mind-runner.maintenance.plist")
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/bin/mind-runner", "maintenance", "--daily", "--daily", "<integer>30</integer>", "launchd.out.log", "RunAtLoad"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("plist thiếu %q", want)
		}
	}

	if len(fr.calls) != 2 {
		t.Fatalf("calls=%v", fr.calls)
	}
	if fr.calls[0][0] != "launchctl" || fr.calls[0][1] != "bootout" {
		t.Fatalf("call 0=%v", fr.calls[0])
	}
	if fr.calls[1][0] != "launchctl" || fr.calls[1][1] != "bootstrap" {
		t.Fatalf("call 1=%v", fr.calls[1])
	}
	if got := fr.calls[1][len(fr.calls[1])-1]; got != plist {
		t.Fatalf("bootstrap arg cuối=%q, muốn %q", got, plist)
	}
}

func TestInstallNonDarwinNoop(t *testing.T) {
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })

	home := t.TempDir()
	fr := &fakeRunner{}
	if err := Install(context.Background(), fr, "/bin/mind-runner", home, filepath.Join(home, "logs")); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls=%v", fr.calls)
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); !os.IsNotExist(err) {
		t.Fatal("non-darwin không được ghi gì")
	}
}

func TestUninstallDarwin(t *testing.T) {
	old := goos
	goos = "darwin"
	t.Cleanup(func() { goos = old })

	home := t.TempDir()
	fr := &fakeRunner{}
	if err := Install(context.Background(), fr, "/bin/mind-runner", home, filepath.Join(home, "logs")); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil
	if err := Uninstall(context.Background(), fr, home); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 || fr.calls[0][1] != "bootout" {
		t.Fatalf("calls=%v", fr.calls)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.mind-runner.maintenance.plist")
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatal("plist chưa được xoá")
	}
}

func TestUninstallNonDarwinNoop(t *testing.T) {
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })
	fr := &fakeRunner{}
	if err := Uninstall(context.Background(), fr, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls=%v", fr.calls)
	}
}
