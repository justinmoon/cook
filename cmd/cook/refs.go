package main

import (
	"fmt"
	"strings"
)

// parseRef splits "owner/repo/name" into (repoRef, name).
// repoRef is "owner/repo", name is the branch/task/etc name.
// If less than 2 slashes, returns ("", arg).
func parseRef(arg string) (repoRef, name string) {
	// Find the last slash to split repo ref from item name
	idx := strings.LastIndex(arg, "/")
	if idx == -1 {
		return "", arg
	}
	repoRef = arg[:idx]
	name = arg[idx+1:]
	// repoRef must contain at least one slash (owner/repo)
	if !strings.Contains(repoRef, "/") {
		return "", arg
	}
	return repoRef, name
}

// requireRef parses "owner/repo/name" or returns error if invalid format
func requireRef(arg, refType string) (repoRef, name string, err error) {
	repoRef, name = parseRef(arg)
	if repoRef == "" {
		return "", "", fmt.Errorf("%s must be in 'owner/repo/%s' format", refType, refType)
	}
	if name == "" {
		return "", "", fmt.Errorf("%s name cannot be empty", refType)
	}
	return repoRef, name, nil
}
