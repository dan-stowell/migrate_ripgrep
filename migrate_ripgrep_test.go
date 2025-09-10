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
		testName := *model+target
		t.Run(testName, func(t *testing.T) {
			log.Printf("Attempting bazel query for target: %s in %s (model: %s)", target, tempDir, *model)
			queryCmd := exec.Command("bazel", "query", target)
			queryCmd.Dir = tempDir
			queryOut, queryErr := queryCmd.CombinedOutput()
			if queryErr != nil {
				t.Errorf("bazel query failed for target %s: %v\n%s", target, queryErr, string(queryOut))
				return
			}
			log.Printf("bazel query succeeded for target: %s", target)

			log.Printf("Attempting bazel build for target: %s in %s", target, tempDir)
			buildCmd := exec.Command("bazel", "build", target)
			buildCmd.Dir = tempDir
			buildOut, buildErr := buildCmd.CombinedOutput()
			if buildErr != nil {
				t.Errorf("bazel build failed for target %s: %v\n%s", target, buildErr, string(buildOut))
				return
			}
			log.Printf("bazel build succeeded for target: %s", target)
		})
	}
}
