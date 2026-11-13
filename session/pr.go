package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PullRequest is a pull request a session opened, and what the PR watcher has already
// told the session about it.
type PullRequest struct {
	URL    string `json:"url"`
	Repo   string `json:"repo"` // owner/name
	Number int    `json:"number"`
	// FailedSHA is the head commit whose failing CI the session was last told about.
	FailedSHA string `json:"failed_sha,omitempty"`
	// Closed means there is nothing more to tell: the merge was announced (or happened
	// before the watcher started), or the PR was closed without merging.
	Closed bool `json:"closed,omitempty"`
}

// ClaudeProjectDir is where Claude Code keeps the transcripts of conversations run in
// dir: <config dir>/projects/<dir with every non-alphanumeric character replaced by '-'>.
func ClaudeProjectDir(dir string) (string, error) {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".claude")
	}
	project := []byte(dir)
	for k, c := range project {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			project[k] = '-'
		}
	}
	return filepath.Join(configDir, "projects", string(project)), nil
}

// PRScanner finds the pull requests sessions opened. Claude Code writes a "pr-link"
// entry into the transcript whenever the conversation's branch has a PR. Transcripts
// only grow, so each file is read from where the last scan stopped. Not safe for
// concurrent use.
type PRScanner struct {
	files map[string]*scannedTranscript
}

type scannedTranscript struct {
	offset int64
	prs    []PullRequest
}

func NewPRScanner() *PRScanner {
	return &PRScanner{files: make(map[string]*scannedTranscript)}
}

// Scan returns the PRs recorded in the transcripts of conversations run in worktreeDir,
// oldest first, each once.
func (s *PRScanner) Scan(worktreeDir string) ([]PullRequest, error) {
	dir, err := ClaudeProjectDir(worktreeDir)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []PullRequest
	seen := make(map[string]bool)
	var errs []error
	for _, path := range paths {
		if err := s.scanFile(path); err != nil {
			errs = append(errs, err)
		}
		if f := s.files[path]; f != nil {
			for _, pr := range f.prs {
				if !seen[pr.URL] {
					seen[pr.URL] = true
					out = append(out, pr)
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

var prLinkMarker = []byte(`"pr-link"`)

func (s *PRScanner) scanFile(path string) error {
	f := s.files[path]
	if f == nil {
		f = &scannedTranscript{}
		s.files[path] = f
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() < f.offset {
		*f = scannedTranscript{} // rewritten: start over
	}
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A last line without its newline is still being written; read it next time.
			if err == io.EOF {
				return nil
			}
			return err
		}
		f.offset += int64(len(line))
		if !bytes.Contains(line, prLinkMarker) {
			continue
		}
		var entry struct {
			Type   string `json:"type"`
			Number int    `json:"prNumber"`
			URL    string `json:"prUrl"`
			Repo   string `json:"prRepository"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "pr-link" || entry.URL == "" {
			continue
		}
		known := false
		for _, pr := range f.prs {
			known = known || pr.URL == entry.URL
		}
		if !known && strings.Count(entry.Repo, "/") == 1 && entry.Number > 0 {
			f.prs = append(f.prs, PullRequest{URL: entry.URL, Repo: entry.Repo, Number: entry.Number})
		}
	}
}
