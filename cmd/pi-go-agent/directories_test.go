package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/peterw22/forge/internal/agent"
	"github.com/peterw22/forge/internal/session"
)

func TestListDirectoriesNamesOnlyDirectories(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gamma", "Alpha", ".hidden", "beta"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "beta"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}

	listing, err := listDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	// Names sort without regard to case.
	want := []string{".hidden", "Alpha", "beta", "gamma", "link"}
	if !reflect.DeepEqual(listing.names, want) {
		t.Fatalf("names = %q, want %q", listing.names, want)
	}
	if listing.path != root || listing.parent != filepath.Dir(root) || listing.cut {
		t.Fatalf("listing = %#v", listing)
	}
}

func TestListDirectoriesRejectsWhatIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", file, filepath.Join(root, "missing")} {
		if _, err := listDirectories(path); err == nil {
			t.Fatalf("listing %q succeeded", path)
		}
	}
}

func TestListDirectoriesOfTheRootHasNoParent(t *testing.T) {
	listing, err := listDirectories(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if listing.parent != "" {
		t.Fatalf("parent = %q", listing.parent)
	}
}

func newDirectoryTestRegistry(t *testing.T, root string) (*runtimeRegistry, *sessionRuntime) {
	t.Helper()
	factory := func(workspace, model, thinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error) {
		core, err := agent.New(agent.Config{Model: model, Thinking: thinking, WorkingDirectory: workspace, Provider: immediateTestProvider{}})
		if err == nil {
			core.Restore(messages, model, thinking, usage)
		}
		return core, err
	}
	core, err := factory(root, "gpt-5.6-terra", "high", nil, agent.Usage{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewAt(root, root, "gpt-5.6-terra", "high")
	if err != nil {
		t.Fatal(err)
	}
	initial := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	registry := newRuntimeRegistry(root, initial, factory)
	t.Cleanup(registry.Shutdown)
	return registry, initial
}

func TestNewSessionWorksInTheChosenDirectory(t *testing.T) {
	root := t.TempDir()
	chosen := filepath.Join(root, "project")
	if err := os.Mkdir(chosen, 0700); err != nil {
		t.Fatal(err)
	}
	registry, initial := newDirectoryTestRegistry(t, root)

	created, err := registry.NewIn(initial, chosen)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, workspace := created.core.Settings(); workspace != chosen {
		t.Fatalf("workspace = %q, want %q", workspace, chosen)
	}
	// The session it was created from keeps its own directory.
	if _, _, workspace := initial.core.Settings(); workspace != root {
		t.Fatalf("original workspace = %q, want %q", workspace, root)
	}
	// The choice is part of the session and survives a reload.
	_, header, _, _, err := session.Resume(created.sessions.Path())
	if err != nil {
		t.Fatal(err)
	}
	if header.CWD != chosen {
		t.Fatalf("stored directory = %q, want %q", header.CWD, chosen)
	}

	inherited, err := registry.NewIn(created, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, workspace := inherited.core.Settings(); workspace != chosen {
		t.Fatalf("inherited workspace = %q, want %q", workspace, chosen)
	}
}

func TestNewSessionRejectsADirectoryThatDoesNotExist(t *testing.T) {
	root := t.TempDir()
	registry, initial := newDirectoryTestRegistry(t, root)
	if _, err := registry.NewIn(initial, filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}
