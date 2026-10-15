// Package main validates fork contribution policy without external dependencies.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

type policyError string

func (e policyError) Error() string { return string(e) }

const errPolicy policyError = "invalid contribution policy"

func validateTitle(title string) error {
	pattern := `^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9._/-]+\))?!?: \S.*$`
	if !regexp.MustCompile(pattern).MatchString(title) {
		return fmt.Errorf("%w: invalid Conventional Commit title: %q", errPolicy, title)
	}

	_, description, _ := strings.Cut(title, ": ")
	if regexp.MustCompile(`(?i)\b(add|update|fix|remove|create|enable)\b`).MatchString(description) {
		return fmt.Errorf("%w: commit description must be a declarative noun phrase: %q", errPolicy, title)
	}

	return nil
}

type changelogState struct {
	unreleased   int
	category     bool
	bullet       bool
	previousDate time.Time
}

func releaseDate(line string, previous time.Time) (time.Time, error) {
	const core = `(0|[1-9][0-9]*)`

	pattern := `^## \[` + core + `\.` + core + `\.` + core +
		`(-[0-9A-Za-z.-]+)?\] - ([0-9]{4}-[0-9]{2}-[0-9]{2})( \[YANKED\])?$`

	match := regexp.MustCompile(pattern).FindStringSubmatch(line)
	if match == nil {
		return time.Time{}, fmt.Errorf("%w: invalid release heading: %q", errPolicy, line)
	}

	date, err := time.Parse(time.DateOnly, match[5])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid release date: %w", err)
	}

	if !previous.IsZero() && date.After(previous) {
		return time.Time{}, fmt.Errorf("%w: release dates must be newest first", errPolicy)
	}

	return date, nil
}

func (s *changelogState) heading(line string) error {
	if s.category && !s.bullet {
		return fmt.Errorf("%w: empty changelog category before %q", errPolicy, line)
	}

	s.category, s.bullet = false, false
	if line == "## [Unreleased]" {
		s.unreleased++
		if s.unreleased != 1 || !s.previousDate.IsZero() {
			return fmt.Errorf("%w: Unreleased must occur once before releases", errPolicy)
		}

		return nil
	}

	if strings.HasPrefix(line, "## ") {
		if s.unreleased != 1 {
			return fmt.Errorf("%w: missing Unreleased before release", errPolicy)
		}

		date, err := releaseDate(line, s.previousDate)
		if err != nil {
			return err
		}

		s.previousDate = date

		return nil
	}

	switch line {
	case "### Added", "### Changed", "### Deprecated", "### Removed", "### Fixed", "### Security":
		s.category = true
	default:
		return fmt.Errorf("%w: invalid changelog category: %q", errPolicy, line)
	}

	return nil
}

func validatePreamble(content string) error {
	if !strings.HasPrefix(content, "# Changelog\n") ||
		!strings.Contains(content, "https://keepachangelog.com/en/1.1.0/") ||
		!strings.Contains(content, "https://semver.org/spec/v2.0.0.html") {
		return fmt.Errorf("%w: changelog requires its title and Keep a Changelog/SemVer references", errPolicy)
	}

	return nil
}

func inheritedHeadings(legacy string) map[string]bool {
	allowed := map[string]bool{}

	for line := range strings.SplitSeq(legacy, "\n") {
		if strings.HasPrefix(line, "## ") {
			allowed[line] = true
		}
	}

	return allowed
}

func validateChangelog(content, legacy string) error {
	err := validatePreamble(content)
	if err != nil {
		return err
	}

	allowed := inheritedHeadings(legacy)

	var state changelogState

	for line := range strings.SplitSeq(content, "\n") {
		if allowed[line] {
			break
		}

		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			err := state.heading(line)
			if err != nil {
				return err
			}
		}

		if strings.HasPrefix(line, "- ") && strings.TrimSpace(strings.TrimPrefix(line, "- ")) != "" && state.category {
			state.bullet = true
		}
	}

	if state.unreleased != 1 || (state.category && !state.bullet) {
		return fmt.Errorf("%w: missing Unreleased or empty final category", errPolicy)
	}

	_, section, _ := strings.Cut(content, "## [Unreleased]")

	section, _, _ = strings.Cut(section, "\n## ")
	if !strings.Contains(section, "\n### ") || !strings.Contains(section, "\n- ") {
		return fmt.Errorf("%w: Unreleased requires a categorized impact entry", errPolicy)
	}

	return nil
}

func run() error {
	content, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}

	legacy, err := os.ReadFile(".ci/changelog-legacy.txt")
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}

	err = validateChangelog(string(content), string(legacy))
	if err != nil {
		return err
	}

	for _, variable := range []string{"GOVERNANCE_TITLE", "GOVERNANCE_TITLES"} {
		value := os.Getenv(variable)
		if value == "" {
			continue
		}

		for title := range strings.SplitSeq(value, "\n") {
			err := validateTitle(title)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func main() {
	err := run()
	if err != nil {
		_, writeErr := fmt.Fprintln(os.Stderr, err)
		if writeErr != nil {
			os.Exit(1)
		}

		os.Exit(1)
	}

	_, err = fmt.Fprintln(os.Stdout, "Conventional Commits and Keep a Changelog: PASS")
	if err != nil {
		os.Exit(1)
	}
}
