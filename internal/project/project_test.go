package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOf(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "acme-api")
	sub := filepath.Join(repo, "internal", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".claude", "worktrees", "feat")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"),
		[]byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "feat")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "notes")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for dir, want := range map[string]string{
		repo: "acme-api", sub: "acme-api", wt: "acme-api", plain: "notes",
		"": "", "/": "", home: "",
	} {
		if got := Of(dir); got != want {
			t.Errorf("Of(%q)=%q want %q", dir, got, want)
		}
	}
}
