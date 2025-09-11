package main_test

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var (
	attempts = flag.Uint("attempts", 3, "number of attempts to build a target")
)

func runCombined(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func gitClone(t *testing.T, repoURL, dest string) {
	if _, err := runCombined("", "git", "clone", "--depth", "1", "--single-branch", repoURL, dest); err != nil {
		t.Fatalf("failed to clone repo %q to %q: %s", repoURL, dest, err)
	}
}

func mkdirTemp(t *testing.T, pattern string) string {
	temp, err := os.MkdirTemp("", pattern)
	if err != nil {
		t.Fatalf("Failed to create temp dir for pattern %q: %s", pattern, err)
	}
	t.Cleanup(func() { os.RemoveAll(temp) })
	return temp
}

func setupAider(t *testing.T) (string, string) {
	aider, err := runfiles.Rlocation("_main/aider")
	if err != nil {
		t.Fatalf("Could not find aider: %s", err)
	}
	if _, err := os.Stat(aider); err != nil {
		t.Fatalf("aider does not exist: %s", err)
	}
	aiderTemp := mkdirTemp(t, "aider")
	return aider, aiderTemp
}

func runAider(t *testing.T, dir, aider, aiderHome, model, prompt string) ([]byte, error) {
	cmd := exec.Command(
		aider,
		"--model", model,
		"--edit-format", "diff",
		"--yes-always",
		"--disable-playwright",
		"--message", prompt,
	)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%q", aiderHome))
	return cmd.CombinedOutput()
}

func aiderCommit(t *testing.T, dir, aider, aiderHome, model string) {
	cmd := exec.Command(
		aider,
		"--commit",
		"--model", model,
	)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%q", aiderHome))
	if _, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Could not commit with aider: %s", err)
	}
}

func buildEditLoop(t *testing.T, repoTemp, target, aider, aiderTemp, model string) bool {
	for attempt := uint(0); attempt < *attempts; attempt++ {
		bazelBuildOutput, err := runCombined(repoTemp, "bazel", "build", target)
		if err == nil {
			t.Logf("bazel build %q succeeded, continuing to next target", target)
			return true
		}
		prompt := fmt.Sprintf(`
			I would like to migrate this repo to build with Bazel.
			I am working target-by-target.
			Right now I am trying to get the %q target to build.
			Can you make the minimal changes necessary to get this target to build?
			Here is the output from the latest 'bazel build %s':

			%s`,
			target, target, bazelBuildOutput,
		)
		if aiderOutput, err := runAider(t, repoTemp, aider, aiderTemp, model, prompt); err != nil {
			t.Fatalf("Error running aider (%s):\n%s", err, aiderOutput)
		}
	}

	bazelBuildOutput, err := runCombined(repoTemp, "bazel", "build", target)
	if err == nil {
		t.Logf("bazel build %q succeeded, continuing to next target", target)
		return true
	}
	t.Logf("last bazel build %q failed, output:\n%s", target, bazelBuildOutput)
	return false
}

func commitSha(t *testing.T, dir string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Could not find commit sha: %s", err)
	}
	return strings.TrimSpace(string(output))
}

func diffFromSha(t *testing.T, dir, sha string) []byte {
	cmd := exec.Command("git", "diff", sha, "HEAD")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Error during git diff %q HEAD: %s", sha, err)
	}
	return output
}

func testMigrateRepo(t *testing.T, repoURL, model string, targets []string) {
	aider, aiderTemp := setupAider(t)
	repoTemp := mkdirTemp(t, regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(repoURL, "-"))
	gitClone(t, repoURL, repoTemp)
	initialSha := commitSha(t, repoTemp)
	for _, target := range targets {
		t.Run(model+target, func(t *testing.T) {
			buildSucceeded := buildEditLoop(t, repoTemp, target, aider, aiderTemp, model)
			aiderCommit(t, repoTemp, aider, aiderTemp, model)
			diff := diffFromSha(t, repoTemp, initialSha)
			t.Logf("Changes made in the build-edit loop:\n%s", diff)
			if !buildSucceeded {
				t.Fatalf("Could not build %q successfully", target)
			}
		})
	}
}

func testMigrateRipgrep(t *testing.T, model string) {
	repoURL := "https://github.com/dan-stowell/ripgrep"
	targets := []string{
		"//crates/matcher:grep_matcher",
		/*
			"//crates/matcher:integration_test",
			"//crates/globset:globset",
			"//crates/cli:grep_cli",
			"//crates/regex:grep_regex",
			"//crates/searcher:grep_searcher",
			"//crates/pcre2:grep_pcre2",
			"//crates/ignore:ignore",
			"//crates/printer:grep_printer",
			"//crates/grep:grep",
			"//:ripgrep",
			"//:integration_test",
		*/
	}
	testMigrateRepo(t, repoURL, model, targets)
}

func TestMigrateRipgrepOpenAIGPT41Mini(t *testing.T) {
	testMigrateRipgrep(t, "openrouter/openai/gpt-4.1-mini")
}
