package main

import (
	"strings"
	"testing"
)

func TestTitles(t *testing.T) {
	t.Parallel()

	for _, title := range []string{
		"fix(http): безопасная диагностика",
		"build(go)!: новый минимум языка",
		"docs: план модернизации",
	} {
		err := validateTitle(title)
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, title := range []string{"plain title", "feat: add client", "fix(http): ", "feat: ", "docs: update rules"} {
		err := validateTitle(title)
		if err == nil {
			t.Fatalf("accepted invalid title %q", title)
		}
	}
}

func TestChangelog(t *testing.T) {
	t.Parallel()

	const valid = "# Changelog\nhttps://keepachangelog.com/en/1.1.0/\n" +
		"https://semver.org/spec/v2.0.0.html\n\n## [Unreleased]\n\n### Added\n\n- Effective governance.\n"

	cases := []struct {
		name, content, legacy string
		valid                 bool
	}{
		{"valid", valid, "", true},
		{"missing unreleased", strings.ReplaceAll(valid, "## [Unreleased]", ""), "", false},
		{"unknown category", strings.ReplaceAll(valid, "### Added", "### Other"), "", false},
		{"empty category", strings.ReplaceAll(valid, "- Effective governance.", ""), "", false},
		{"invalid date", valid + "\n## [1.0.0] - 2026-02-30\n", "", false},
		{"legacy inherited", valid + "\n## [v0.6.1]\n### Added\n- Historical entry.\n", "## [v0.6.1]\n", true},
		{"unknown legacy", valid + "\n## [v9.9.9]\n", "", false},
		{"duplicate unreleased", valid + "\n## [Unreleased]\n", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := validateChangelog(testCase.content, testCase.legacy)
			if (err == nil) != testCase.valid {
				t.Fatalf("valid=%v, error=%v", testCase.valid, err)
			}
		})
	}
}
