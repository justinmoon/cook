package repo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repo struct {
	Owner string // pubkey of owner
	Name  string // repo name
	Path  string // filesystem path
}

// FullName returns owner/name format
func (r *Repo) FullName() string {
	return r.Owner + "/" + r.Name
}

type Store struct {
	reposDir string
}

func NewStore(dataDir string) *Store {
	return &Store{
		reposDir: filepath.Join(dataDir, "repos"),
	}
}

// List returns all repos, optionally filtered by owner
func (s *Store) List(owner string) ([]Repo, error) {
	var repos []Repo

	if owner != "" {
		// List repos for specific owner
		ownerDir := filepath.Join(s.reposDir, owner)
		entries, err := os.ReadDir(ownerDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}

		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".git")
			repos = append(repos, Repo{
				Owner: owner,
				Name:  name,
				Path:  filepath.Join(ownerDir, entry.Name()),
			})
		}
	} else {
		// List all repos across all owners
		ownerDirs, err := os.ReadDir(s.reposDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}

		for _, ownerEntry := range ownerDirs {
			if !ownerEntry.IsDir() {
				continue
			}
			ownerName := ownerEntry.Name()
			ownerPath := filepath.Join(s.reposDir, ownerName)

			repoEntries, err := os.ReadDir(ownerPath)
			if err != nil {
				continue
			}

			for _, repoEntry := range repoEntries {
				if !repoEntry.IsDir() || !strings.HasSuffix(repoEntry.Name(), ".git") {
					continue
				}
				name := strings.TrimSuffix(repoEntry.Name(), ".git")
				repos = append(repos, Repo{
					Owner: ownerName,
					Name:  name,
					Path:  filepath.Join(ownerPath, repoEntry.Name()),
				})
			}
		}
	}

	return repos, nil
}

// Get returns a repo by owner and name
func (s *Store) Get(owner, name string) (*Repo, error) {
	path := filepath.Join(s.reposDir, owner, name+".git")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	return &Repo{Owner: owner, Name: name, Path: path}, nil
}

// Create creates a new repo for the given owner with a template commit
func (s *Store) Create(owner, name string) (*Repo, error) {
	ownerDir := filepath.Join(s.reposDir, owner)
	path := filepath.Join(ownerDir, name+".git")

	// Check if already exists
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("repository %s/%s already exists", owner, name)
	}

	// Ensure owner directory exists
	if err := os.MkdirAll(ownerDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create owner directory: %w", err)
	}

	// Create bare repo
	cmd := exec.Command("git", "init", "--bare", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init failed: %s: %w", string(output), err)
	}

	// Set default branch to master
	cmd = exec.Command("git", "-C", path, "symbolic-ref", "HEAD", "refs/heads/master")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to set default branch: %s: %w", string(output), err)
	}

	repo := &Repo{Owner: owner, Name: name, Path: path}

	// Create initial template commit so master exists
	if err := s.createTemplateCommit(repo); err != nil {
		// Clean up on failure
		os.RemoveAll(path)
		return nil, fmt.Errorf("failed to create template commit: %w", err)
	}

	return repo, nil
}

// createTemplateCommit creates an initial commit with template files
func (s *Store) createTemplateCommit(repo *Repo) error {
	// Create temp workdir
	tmpDir, err := os.MkdirTemp("", "cook-template-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the bare repo
	cmd := exec.Command("git", "clone", repo.Path, tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	// Create template files
	readmeContent := fmt.Sprintf("# %s\n\nA new Cook repository.\n", repo.Name)
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte(readmeContent), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	cookTomlContent := `# Cook configuration
[[gates]]
name = "test"
command = "echo 'No tests configured'"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "cook.toml"), []byte(cookTomlContent), 0644); err != nil {
		return fmt.Errorf("failed to write cook.toml: %w", err)
	}

	// Git add
	cmd = exec.Command("git", "-C", tmpDir, "add", "-A")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// Git commit
	cmd = exec.Command("git", "-C", tmpDir, "commit", "-m", "Initial commit", "--author", "Cook <cook@local>")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=Cook", "GIT_COMMITTER_EMAIL=cook@local")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	// Push to bare repo
	cmd = exec.Command("git", "-C", tmpDir, "push", "origin", "master")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %s: %w", string(output), err)
	}

	return nil
}

// Clone clones a repo from URL for the given owner
func (s *Store) Clone(owner, name, url string) (*Repo, error) {
	ownerDir := filepath.Join(s.reposDir, owner)
	path := filepath.Join(ownerDir, name+".git")

	// Check if already exists
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("repository %s/%s already exists", owner, name)
	}

	// Ensure owner directory exists
	if err := os.MkdirAll(ownerDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create owner directory: %w", err)
	}

	// Clone as bare repo
	cmd := exec.Command("git", "clone", "--bare", url, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	return &Repo{Owner: owner, Name: name, Path: path}, nil
}

// Remove removes a repo
func (s *Store) Remove(owner, name string) error {
	path := filepath.Join(s.reposDir, owner, name+".git")

	// Check if exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("repository %s/%s not found", owner, name)
	}

	return os.RemoveAll(path)
}

// ParseRepoRef parses "owner/name" into components
func ParseRepoRef(ref string) (owner, name string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo ref %q, expected owner/name", ref)
	}
	return parts[0], parts[1], nil
}
