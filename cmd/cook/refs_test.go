package main

import (
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		input    string
		wantRepo string
		wantName string
	}{
		// Valid cases
		{"owner/repo/branch", "owner/repo", "branch"},
		{"alice/myrepo/feature-x", "alice/myrepo", "feature-x"},
		{"org/project/fix-bug-123", "org/project", "fix-bug-123"},

		// Edge cases - no valid repo ref
		{"branch", "", "branch"},
		{"repo/branch", "", "repo/branch"},
		{"", "", ""},

		// Multiple slashes in name not allowed (parseRef takes last slash)
		{"owner/repo/some/nested/path", "owner/repo/some/nested", "path"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			repo, name := parseRef(tc.input)
			if repo != tc.wantRepo || name != tc.wantName {
				t.Errorf("parseRef(%q) = (%q, %q), want (%q, %q)",
					tc.input, repo, name, tc.wantRepo, tc.wantName)
			}
		})
	}
}

func TestRequireRef(t *testing.T) {
	tests := []struct {
		input   string
		refType string
		wantErr bool
	}{
		// Valid
		{"owner/repo/branch", "branch", false},
		{"alice/project/task-1", "task", false},

		// Invalid - no repo ref
		{"branch", "branch", true},
		{"repo/branch", "branch", true},

		// Invalid - empty name (trailing slash)
		{"owner/repo/", "branch", true},

		// Invalid - empty input
		{"", "branch", true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			_, _, err := requireRef(tc.input, tc.refType)
			if (err != nil) != tc.wantErr {
				t.Errorf("requireRef(%q, %q) error = %v, wantErr %v",
					tc.input, tc.refType, err, tc.wantErr)
			}
		})
	}
}
