package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRootFileOperationApplierWritesAndDeletes(t *testing.T) {
	root := t.TempDir()
	deleted := filepath.Join(root, "old.txt")
	if err := os.WriteFile(deleted, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	applier := RootFileOperationApplier{Root: root}
	err := applier.Apply(context.Background(), []FileOperation{
		{Operation: "write", Path: "pkg/new.go", Content: "package pkg\n"},
		{Operation: "delete", Path: "old.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "pkg", "new.go"))
	if err != nil || string(b) != "package pkg\n" {
		t.Fatalf("written file = %q, %v", b, err)
	}
	if _, err := os.Stat(deleted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file remains: %v", err)
	}
}

func TestRootFileOperationApplierValidatesBatchBeforeWriting(t *testing.T) {
	root := t.TempDir()
	applier := RootFileOperationApplier{Root: root}
	err := applier.Apply(context.Background(), []FileOperation{
		{Operation: "write", Path: "would-have-been-written.txt", Content: "data"},
		{Operation: "write", Path: "../outside.txt", Content: "escape"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes repository") {
		t.Fatalf("Apply error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "would-have-been-written.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("batch mutated before validation completed: %v", err)
	}
}

func TestRootFileOperationApplierRejectsProtectedAndDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	applier := RootFileOperationApplier{Root: root}
	for _, path := range []string{".git/config", ".lbai/state.json", ".lbai/snapshots/run.json"} {
		if err := applier.Apply(context.Background(), []FileOperation{{Operation: "write", Path: path, Content: "bad"}}); err == nil {
			t.Fatalf("Apply accepted protected path %q", path)
		}
	}
	err := applier.Apply(context.Background(), []FileOperation{
		{Operation: "write", Path: "same.txt", Content: "one"},
		{Operation: "write", Path: "same.txt", Content: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate path") {
		t.Fatalf("Apply duplicate error = %v", err)
	}
}

func TestRootFileOperationApplierRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires additional Windows privileges: %v", err)
		}
		t.Fatal(err)
	}
	err := (RootFileOperationApplier{Root: root}).Apply(context.Background(), []FileOperation{{Operation: "write", Path: "link/escaped.txt", Content: "bad"}})
	if err == nil {
		t.Fatal("Apply followed a symlink outside the repository")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file was created: %v", err)
	}
}

func TestRootFileOperationApplierHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (RootFileOperationApplier{Root: t.TempDir()}).Apply(ctx, []FileOperation{{Operation: "write", Path: "file.txt", Content: "data"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v", err)
	}
}
