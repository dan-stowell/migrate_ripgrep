package main_test

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var (
	attempts = flag.Int("attempts", 3, "number of attempts to build a target")
)

func runCombined(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func gitClone(t *testing.T, repoURL, dest string) {
	t.Logf("cloning %q", repoURL)
	u, err := url.Parse(repoURL)
	if err != nil {
		t.Fatalf("Could not parse url %q: %s", repoURL, err)
	}
	username, ok := os.LookupEnv("GITHUB_USERNAME")
	if !ok {
		t.Fatal("Did not find GITHUB_USERNAME in env")
	}
	token, ok := os.LookupEnv("GITHUB_TOKEN")
	if !ok {
		t.Fatal("Did not find GITHUB_TOKEN in env")
	}
	u.User = url.UserPassword(username, token)
	if _, err := runCombined("", "git", "clone", "--depth", "1", "--single-branch", u.String(), dest); err != nil {
		t.Fatalf("Failed to clone repo %q to %q: %s", repoURL, dest, err)
	}
	t.Logf("successfully cloned %q", repoURL)
}

func gitBranch(t *testing.T, model, dir string) string {
	t.Log("checking out fresh git branch")
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	branch := model + "-" + ts
	if _, err := runCombined(dir, "git", "checkout", "-b", branch); err != nil {
		t.Fatalf("Could not checkout branch %q: %s", branch, err)
	}
	t.Logf("successfully checked out branch %q", branch)
	return branch
}

func mkdirTemp(t *testing.T, pattern string) string {
	t.Logf("making temp dir for %q", pattern)
	temp, err := os.MkdirTemp("", pattern)
	if err != nil {
		t.Fatalf("Failed to create temp dir for pattern %q: %s", pattern, err)
	}
	t.Cleanup(func() { os.RemoveAll(temp) })
	t.Logf("successfully made temp dir for %q", pattern)
	return temp
}

func setupAider(t *testing.T) (string, string) {
	t.Log("setting up aider")
	aider, err := runfiles.Rlocation("_main/aider")
	if err != nil {
		t.Fatalf("Could not find aider: %s", err)
	}
	if _, err := os.Stat(aider); err != nil {
		t.Fatalf("aider does not exist: %s", err)
	}
	aiderTemp := mkdirTemp(t, "aider")
	t.Log("successfully set up aider")
	return aider, aiderTemp
}

func runAider(t *testing.T, dir, aider, aiderHome, model, prompt, buildFile string) ([]byte, error) {
	t.Logf("running aider with model %q", model)
	cmd := exec.Command(
		aider,
		"--no-check-update",
		"--no-show-release-notes",
		"--model", model,
		"--edit-format", "diff",
		"--yes-always",
		"--disable-playwright",
		"--file", buildFile,
		"--read", "MODULE.bazel",
		"--message", prompt,
	)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%q", aiderHome))
	return cmd.CombinedOutput()
}

func aiderCommit(t *testing.T, dir, aider, aiderHome, model string) {
	t.Logf("committing code using aider and model %q", model)
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
	t.Logf("successfully commited code using aider and model %q", model)
}

func relDirForTarget(target string) string {
	return strings.TrimPrefix(strings.Split(target, ":")[0], "//")
}

func ensureBuildBazelExists(t *testing.T, dir, target string) string {
	t.Logf("ensuring BUILD.bazel exists for target %q", target)
	targetDir := relDirForTarget(target)
	if _, err := os.Stat(filepath.Join(dir, targetDir)); err != nil {
		t.Fatalf("Directory %s for target %q does not exist: %s", targetDir, target, err)
	}
	buildBazelPath := filepath.Join(dir, targetDir, "BUILD.bazel")
	_, err := os.Stat(buildBazelPath)
	if err == nil {
		return filepath.Join(targetDir, "BUILD.bazel")
	}
	f, err := os.Create(buildBazelPath)
	if err != nil {
		t.Fatalf("Could not create %s: %s", buildBazelPath, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Could not close %s: %s", buildBazelPath, err)
	}
	t.Logf("successfully ensured %q exists for target %q", filepath.Join(targetDir, "BUILD.bazel"), target)
	return filepath.Join(targetDir, "BUILD.bazel")
}

func buildEditLoop(t *testing.T, repoTemp, target, aider, aiderTemp, model, buildBazelPath string) bool {
	for attempt := 0; attempt < *attempts; attempt++ {
		beforeSha := commitSha(t, repoTemp)
		t.Logf("building target %q, sha %s", target, beforeSha)
		bazelBuildOutput, err := runCombined(repoTemp, "bazel", "build", target)
		if err == nil {
			t.Logf("bazel build %q succeeded, continuing to next target", target)
			return true
		}
		t.Logf("bazel build %q did not succeed, invoking aider", target)
		prompt := fmt.Sprintf(`
			I would like to migrate this repo to build with Bazel.
			I am working target-by-target.
			Right now I am trying to get the %q target to build.
			Can you make the minimal changes to %q necessary to get this target to build?
			Here is the output from the latest 'bazel build %s':

			%s`,
			target, buildBazelPath, target, bazelBuildOutput,
		)
		if aiderOutput, err := runAider(t, repoTemp, aider, aiderTemp, model, prompt, buildBazelPath); err != nil {
			t.Fatalf("Error running aider (%s):\n%s", err, aiderOutput)
		}
		afterSha := commitSha(t, repoTemp)
		t.Logf("successfully ran aider, sha %s", afterSha)
		if beforeSha == afterSha {
			t.Log("aider committed no changes")
		}
		t.Logf("changes made by aider:\n%s", diff(t, repoTemp, beforeSha, afterSha))
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

func diff(t *testing.T, dir, left, right string) []byte {
	cmd := exec.Command("git", "diff", left, right)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Error during git diff %q %q: %s", left, right, err)
	}
	return output
}

func isRepoClean(t *testing.T, dir string) bool {
	t.Log("checking if repo is clean")
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Error during git status check (%s):\n%s", err, output)
	}
	isClean := len(output) == 0
	t.Logf("checked if repo is clean: %t", isClean)
	return isClean
}

func testMigrateRepo(t *testing.T, repoURL, model string, targets []string) {
	aider, aiderTemp := setupAider(t)
	repoTemp := mkdirTemp(t, regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(repoURL, "-"))
	gitClone(t, repoURL, repoTemp)
	gitBranch(t, model, repoTemp)
	for _, target := range targets {
		t.Logf("Migrating %q in %q with model %q", target, repoURL, model)
		beforeSha := commitSha(t, repoTemp)
		buildBazelPath := ensureBuildBazelExists(t, repoTemp, target)
		buildSucceeded := buildEditLoop(t, repoTemp, target, aider, aiderTemp, model, buildBazelPath)
		if !isRepoClean(t, repoTemp) {
			aiderCommit(t, repoTemp, aider, aiderTemp, model)
		}
		afterSha := commitSha(t, repoTemp)
		if beforeSha == afterSha {
			t.Log("build-edit loop made no changes, surprising")
		} else {
			t.Logf("Changes made in the build-edit loop:\n%s", diff(t, repoTemp, beforeSha, afterSha))
		}
		if !buildSucceeded {
			t.Fatalf("Could not build %q successfully", target)
		}
	}
}

func testMigrateRipgrep(t *testing.T, model string) {
	repoURL := "https://github.com/dan-stowell/ripgrep"
	targets := []string{
		"//crates/matcher:grep_matcher",
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
	}
	testMigrateRepo(t, repoURL, model, targets)
}

func TestGPT5Mini(t *testing.T) {
	testMigrateRipgrep(t, "openrouter/openai/gpt-5-mini")
}
