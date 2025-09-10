package migrate_ripgrep_test

import (
	"flag"
	"log"
	"os"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var model = flag.String("model", "openrouter/google/gemini-2.5-flash", "The LLM model to use for testing")

var targets = []string{
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

// ensureBuildBazelExists creates an empty BUILD.bazel file if it doesn't exist.
func ensureBuildBazelExists(worktreePath, target string) error {
	// Parse target like //path/to/pkg:target or //:target
	if !strings.HasPrefix(target, "//") {
		// not a package-style target; nothing to do
		return nil
	}
	s := strings.TrimPrefix(target, "//")
	pkg := s
	if idx := strings.Index(s, ":"); idx != -1 {
		pkg = s[:idx]
	}
	var pkgPath string
	if pkg == "" {
		pkgPath = ""
	} else {
		pkgPath = pkg
	}
	buildPath := filepath.Join(worktreePath, pkgPath, "BUILD.bazel")
	if _, err := os.Stat(buildPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", buildPath, err)
	}
	dir := filepath.Dir(buildPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(buildPath, []byte("# created by migrate_ripgrep_test.go\n"), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", buildPath, err)
	}
	log.Printf("Created %s", buildPath)
	return nil
}

// gitStashAll stashes untracked and dirty files.
func gitStashAll(worktreePath string) error {
	// Stash untracked and dirty files so the next aider invocation starts clean.
	stashCmd := exec.Command("git", "stash", "push", "-u", "-m", "aider-temp-stash")
	stashCmd.Dir = worktreePath
	out, err := stashCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git stash failed in %s: %v\n%s", worktreePath, err, string(out))
	}
	// git stash prints a message even when there is nothing to stash;
	// log the output for debugging but don't treat it as fatal.
	log.Printf("git stash output in %s: %s", worktreePath, strings.TrimSpace(string(out)))
	return nil
}

// Helper command runners and high-level wrappers.
func runCombined(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func gitClone(repoURL, dest string) error {
	_, err := runCombined("", "git", "clone", "--depth", "1", "--single-branch", repoURL, dest)
	return err
}

func gitBranch(dir, branchName string) error {
	_, err := runCombined(dir, "git", "branch", branchName)
	return err
}

func gitCheckout(dir, branchName string) error {
	_, err := runCombined(dir, "git", "checkout", branchName)
	return err
}

func bazelQuery(dir, target string) (string, error) {
	return runCombined(dir, "bazel", "query", target)
}

func bazelBuild(dir, target string) (string, error) {
	return runCombined(dir, "bazel", "build", target)
}

func runAider(aiderBin, dir, home, model, target, buildArg string) error {
	args := []string{
		"--disable-playwright",
		"--yes-always",
		"--model", model,
		"--edit-format", "diff",
		"--auto-test",
		"--test-cmd", "bazel build " + target,
		"--message", "Please make the minimal Bazel file changes necessary to build " + target + ". Do not touch non-Bazel files.",
		"MODULE.bazel",
		buildArg,
	}
	cmd := exec.Command(aiderBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aider error: %v\n%s", err, string(out))
	}
	return nil
}

func gitAddAll(dir string) (string, error) {
	return runCombined(dir, "git", "add", "-A")
}

func gitStatusPorcelain(dir string) (string, error) {
	out, err := runCombined(dir, "git", "status", "--porcelain")
	return out, err
}

func gitCommit(dir, msg string) error {
	_, err := runCombined(dir, "git", "commit", "-m", msg)
	return err
}

func gitDiff(dir string) (string, error) {
	return runCombined(dir, "git", "diff")
}

func setupTestMigrate(t *testing.T) (aiderBin, tempDir, aiderTempDir string) {
	t.Helper()
	flag.Parse()

	aiderBin, err := runfiles.Rlocation("_main/aider")
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("aider path: %s", aiderBin)

	// Create a temporary directory for the git clone
	tempDir, err = os.MkdirTemp("", "ripgrep-test-")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) }) // Clean up the temporary directory

	aiderTempDir, err = os.MkdirTemp("", "aider-test-home-")
	if err != nil {
		t.Fatalf("Failed to create temporary directory for aider home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(aiderTempDir) }) // Clean up the aider temporary directory

	repoURL := "https://github.com/dan-stowell/ripgrep"
	log.Printf("Cloning %s into %s", repoURL, tempDir)

	// Clone the repository
	log.Printf("Invoking git clone --depth 1 --single-branch %s %s", repoURL, tempDir)
	if err := gitClone(repoURL, tempDir); err != nil {
		t.Fatalf("Failed to clone repository: %v", err)
	}
	log.Printf("Completed git clone")

	// Get the basename of the temporary directory to use as the branch name
	branchName := filepath.Base(tempDir)
	log.Printf("Invoking git branch %s in %s", branchName, tempDir)

	// Create a new git branch
	if err := gitBranch(tempDir, branchName); err != nil {
		t.Fatalf("Failed to create branch %s: %v", branchName, err)
	}
	log.Printf("Completed git branch %s", branchName)

	// Checkout the new branch
	log.Printf("Invoking git checkout %s in %s", branchName, tempDir)
	if err := gitCheckout(tempDir, branchName); err != nil {
		t.Fatalf("Failed to checkout branch %s: %v", branchName, err)
	}
	log.Printf("Completed git checkout %s", branchName)

	return aiderBin, tempDir, aiderTempDir
}

func TestMigrate(t *testing.T) {
	aiderBin, tempDir, aiderTempDir := setupTestMigrate(t)

	for _, target := range targets {
		testName := *model + target
		t.Run(testName, func(t *testing.T) {
			if err := ensureBuildBazelExists(tempDir, target); err != nil {
				t.Fatalf("Error ensuring BUILD.bazel for target %s: %v", target, err)
			}

			// determine the BUILD.bazel path for the target to pass to aider
			pkg := strings.TrimPrefix(target, "//")
			if idx := strings.Index(pkg, ":"); idx != -1 {
				pkg = pkg[:idx]
			}
			var buildArg string
			if pkg == "" {
				buildArg = "BUILD.bazel"
			} else {
				buildArg = filepath.Join(pkg, "BUILD.bazel")
			}

			// Pre-check: If bazel query then bazel build succeed without changes, skip aider.
			log.Printf("Invoking bazel query %s (model: %s)", target, *model)
			queryOut, queryErr := bazelQuery(tempDir, target)
			if queryErr == nil {
				log.Printf("Completed bazel query %s (model: %s)", target, *model)
				// Query succeeded; try building directly.
				log.Printf("Invoking bazel build %s (model: %s)", target, *model)
				bazelOut, bazelErr := bazelBuild(tempDir, target)
				if bazelErr == nil {
					log.Printf("Completed bazel build %s (model: %s)", target, *model)
					log.Printf("Pre-check: bazel query and build succeeded for model %s target %s; skipping aider", *model, target)
					return // move to next target
				}
				log.Printf("Pre-check: bazel build failed for model %s target %s: %v\n%s", *model, target, bazelErr, bazelOut)
				// Fall through to aider loop to attempt fixes.
			} else {
				log.Printf("Pre-check: bazel query failed for model %s target %s: %v\n%s", *model, target, queryErr, queryOut)
				// Fall through to aider loop to attempt fixes.
			}

			// Try up to N attempts per model/target using aider to produce Bazel changes.
			const maxAttempts = 5
			success := false
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				log.Printf("Invoking aider for model %s target %s (attempt %d/%d)", *model, target, attempt, maxAttempts)
				if err := runAider(aiderBin, tempDir, aiderTempDir, *model, target, buildArg); err != nil {
					log.Printf("aider failed for model %s target %s: %v", *model, target, err)
					continue // Continue to next attempt if aider itself fails
				}
				log.Printf("Completed aider for model %s target %s (attempt %d/%d)", *model, target, attempt, maxAttempts)

				// After aider, first run 'bazel query' to check target visibility/resolution.
				log.Printf("Invoking bazel query %s (model: %s, attempt %d/%d)", target, *model, attempt, maxAttempts)
				queryOut, queryErr := bazelQuery(tempDir, target)
				if queryErr != nil {
					log.Printf("bazel query failed for model %s target %s: %v\n%s", *model, target, queryErr, queryOut)
					continue
				}
				log.Printf("Completed bazel query %s (model: %s, attempt %d/%d)", target, *model, attempt, maxAttempts)

				// Query succeeded; attempt to build the target.
				log.Printf("Invoking bazel build %s (model: %s, attempt %d/%d)", target, *model, attempt, maxAttempts)
				bazelOut, bazelErr := bazelBuild(tempDir, target)
				if bazelErr != nil {
					log.Printf("bazel build failed for model %s target %s: %v\n%s", *model, target, bazelErr, bazelOut)
					continue
				}
				log.Printf("Completed bazel build %s (model: %s, attempt %d/%d)", target, *model, attempt, maxAttempts)

				// Bazel build succeeded. Commit any untracked or dirty files and move on.
				log.Printf("Invoking git add -A in %s", tempDir)
				if out, err := gitAddAll(tempDir); err != nil {
					t.Fatalf("git add failed in %s: %v\n%s", tempDir, err, out)
				}
				log.Printf("Completed git add -A")

				log.Printf("Invoking git status --porcelain in %s", tempDir)
				statusOut, err := gitStatusPorcelain(tempDir)
				if err != nil {
					t.Fatalf("git status failed in %s: %v", tempDir, err)
				}
				log.Printf("Completed git status --porcelain")

				if strings.TrimSpace(string(statusOut)) == "" {
					log.Printf("No changes to commit in %s for model %s target %s", tempDir, *model, target)
				} else {
					commitMsg := fmt.Sprintf("aider: model %s target %s", *model, target)
					log.Printf("Invoking git commit -m \"%s\" in %s", commitMsg, tempDir)
					if err := gitCommit(tempDir, commitMsg); err != nil {
						t.Fatalf("git commit failed in %s: %v", tempDir, err)
					}
					log.Printf("Completed git commit -m \"%s\"", commitMsg)
				}

				log.Printf("bazel build succeeded for model %s target %s", *model, target)
				success = true
				break // move to next target
			}
			var testFailed bool
			if !success {
				log.Printf("Maximum attempts (%d) reached for model %s target %s; test will fail.", maxAttempts, *model, target)
				testFailed = true
			}

			// Print git diff at the end of each test case
			log.Printf("Git diff for model %s target %s:", *model, target)
			diffOut, err := gitDiff(tempDir)
			if err != nil {
				log.Printf("Failed to get git diff in %s: %v\n%s", tempDir, err, diffOut)
			} else {
				log.Printf("\n%s", diffOut)
			}

			if testFailed {
				t.FailNow()
			}
		})
	}
}
