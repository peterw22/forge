package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxListedDirectories bounds one listing sent to a client.
const maxListedDirectories = 2000

type directoryListing struct {
	path   string
	parent string
	home   string
	names  []string
	cut    bool
}

// listDirectories names the directories inside path, so that a client can
// choose the working directory of a session on the agent's machine.
func listDirectories(path string) (directoryListing, error) {
	directory, err := validWorkingDirectory(path)
	if err != nil {
		return directoryListing{}, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return directoryListing{}, err
	}
	listing := directoryListing{path: directory}
	if parent := filepath.Dir(directory); parent != directory {
		listing.parent = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		listing.home = home
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			// A link counts when it leads to a directory.
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Stat(filepath.Join(directory, entry.Name()))
			if err != nil || !target.IsDir() {
				continue
			}
		}
		listing.names = append(listing.names, entry.Name())
	}
	sort.Slice(listing.names, func(i, j int) bool {
		left, right := strings.ToLower(listing.names[i]), strings.ToLower(listing.names[j])
		if left != right {
			return left < right
		}
		return listing.names[i] < listing.names[j]
	})
	if len(listing.names) > maxListedDirectories {
		listing.names = listing.names[:maxListedDirectories]
		listing.cut = true
	}
	return listing, nil
}
