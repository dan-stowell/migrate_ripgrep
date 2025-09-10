package migrate_ripgrep_test

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

func TestMigrate(t *testing.T) {
	flag.Parse()

	aiderBin, err := runfiles.Rlocation("_main/aider")
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("aider path: %s", aiderBin)

	// Create a temporary directory for the git clone
	tempDir, err := os.MkdirTemp("", "ripgrep-test-")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tempDir) // Clean up the temporary directory

	repoURL := "https://github.com/dan-stowell/ripgrep"
	log.Printf("Cloning %s into %s", repoURL, tempDir)

	// Clone the repository
	cloneCmd := exec.Command("git", "clone", "--depth", "1", "--single-branch", repoURL, tempDir)
	cloneCmd.Stdout = os.Stdout
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		t.Fatalf("Failed to clone repository: %v", err)
	}

	// Get the basename of the temporary directory to use as the branch name
	branchName := filepath.Base(tempDir)
	log.Printf("Creating branch %s in %s", branchName, tempDir)

	// Create a new git branch
	branchCmd := exec.Command("git", "branch", branchName)
	branchCmd.Dir = tempDir
	branchCmd.Stdout = os.Stdout
	branchCmd.Stderr = os.Stderr
	if err := branchCmd.Run(); err != nil {
		t.Fatalf("Failed to create branch %s: %v", branchName, err)
	}

	// Checkout the new branch
	checkoutCmd := exec.Command("git", "checkout", branchName)
	checkoutCmd.Dir = tempDir
	checkoutCmd.Stdout = os.Stdout
	checkoutCmd.Stderr = os.Stderr
	if err := checkoutCmd.Run(); err != nil {
		t.Fatalf("Failed to checkout branch %s: %v", branchName, err)
	}

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
			queryCmd := exec.Command("bazel", "query", target)
			queryCmd.Dir = tempDir
			queryOut, queryErr := queryCmd.CombinedOutput()
			if queryErr == nil {
				// Query succeeded; try building directly.
				bazelCmd := exec.Command("bazel", "build", target)
				bazelCmd.Dir = tempDir
				bazelOut, bazelErr := bazelCmd.CombinedOutput()
				if bazelErr == nil {
					log.Printf("bazel query and build succeeded for model %s target %s; skipping aider", *model, target)
					return // move to next target
				}
				log.Printf("Pre-check bazel build failed for model %s target %s: %v\n%s", *model, target, bazelErr, string(bazelOut))
				// Fall through to aider loop to attempt fixes.
			} else {
				log.Printf("Pre-check bazel query failed for model %s target %s: %v\n%s", *model, target, queryErr, string(queryOut))
				// Fall through to aider loop to attempt fixes.
			}

			// Try up to N attempts per model/target using aider to produce Bazel changes.
			const maxAttempts = 5
			success := false
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				aiderCmd := exec.Command(
					aiderBin,
					"--disable-playwright",
					"--yes-always",
					"--model", *model,
					"--edit-format", "diff",
					"--auto-test",
					"--test-cmd", "bazel build "+target,
					"--message", "Please make the minimal Bazel file changes necessary to build "+target+". Do not touch non-Bazel files.",
					"MODULE.bazel",
					buildArg,
				)
				aiderCmd.Dir = tempDir
				aiderCmd.Stdout = os.Stdout
				aiderCmd.Stderr = os.Stderr
				if err := aiderCmd.Run(); err != nil {
					t.Errorf("aider failed for model %s target %s: %v", *model, target, err)
					break // Break from attempts loop if aider itself fails
				}
				log.Printf("aider completed for model %s target %s (attempt %d/%d)", *model, target, attempt, maxAttempts)

				// After aider, first run 'bazel query' to check target visibility/resolution.
				queryCmd := exec.Command("bazel", "query", target)
				queryCmd.Dir = tempDir
				queryOut, queryErr := queryCmd.CombinedOutput()
				if queryErr != nil {
					log.Printf("bazel query failed for model %s target %s: %v\n%s", *model, target, queryErr, string(queryOut))
					// Stash any untracked or dirty files and retry with aider.
					if err := gitStashAll(tempDir); err != nil {
						t.Fatalf("git stash failed in %s: %v", tempDir, err)
					}
					log.Printf("Re-invoking aider for model %s target %s after failed bazel query (attempt %d/%d)", *model, target, attempt, maxAttempts)
					continue
				}

				// Query succeeded; attempt to build the target.
				bazelCmd := exec.Command("bazel", "build", target)
				bazelCmd.Dir = tempDir
				bazelOut, bazelErr := bazelCmd.CombinedOutput()
				if bazelErr != nil {
					log.Printf("bazel build failed for model %s target %s: %v\n%s", *model, target, bazelErr, string(bazelOut))
					// Stash any untracked or dirty files and retry with aider.
					if err := gitStashAll(tempDir); err != nil {
						t.Fatalf("git stash failed in %s: %v", tempDir, err)
					}
					log.Printf("Re-invoking aider for model %s target %s after failed bazel build (attempt %d/%d)", *model, target, attempt, maxAttempts)
					continue
				}

				// Bazel build succeeded. Commit any untracked or dirty files and move on.
				addCmd := exec.Command("git", "add", "-A")
				addCmd.Dir = tempDir
				if out, err := addCmd.CombinedOutput(); err != nil {
					t.Fatalf("git add failed in %s: %v\n%s", tempDir, err, string(out))
				}

				statusCmd := exec.Command("git", "status", "--porcelain")
				statusCmd.Dir = tempDir
				statusOut, err := statusCmd.Output()
				if err != nil {
					t.Fatalf("git status failed in %s: %v", tempDir, err)
				}
				if strings.TrimSpace(string(statusOut)) == "" {
					log.Printf("No changes to commit in %s for model %s target %s", tempDir, *model, target)
				} else {
					commitMsg := fmt.Sprintf("aider: model %s target %s", *model, target)
					commitCmd := exec.Command("git", "commit", "-m", commitMsg)
					commitCmd.Dir = tempDir
					commitCmd.Stdout = os.Stdout
					commitCmd.Stderr = os.Stderr
					if err := commitCmd.Run(); err != nil {
						t.Fatalf("git commit failed in %s: %v", tempDir, err)
					}
					log.Printf("Committed changes in %s: %s", tempDir, commitMsg)
				}

				log.Printf("bazel build succeeded for model %s target %s", *model, target)
				success = true
				break // move to next target
			}
			if !success {
				t.Errorf("Maximum attempts (%d) reached for model %s target %s; moving on to next target/worktree", maxAttempts, *model, target)
			}
		})
	}
}
