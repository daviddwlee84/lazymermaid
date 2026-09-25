package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/boyter/gocodewalker"
)

// Scan discovers sources beneath path. Repository scans honor nested ignore
// files and skip hidden entries unless requested. An explicitly named file is
// always read, even if hidden or ignored. Directory symlinks are not followed.
// Results have absolute paths and deterministic path/line ordering. Individual
// unreadable or malformed files are reported as issues without hiding good rows.
func Scan(ctx context.Context, path string, options ScanOptions) ([]Diagram, []ScanIssue, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("%s: expected a regular file or directory", abs)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, nil, err
		}
		doc, err := Parse(abs, data)
		if err != nil {
			return nil, nil, err
		}
		return doc.Diagrams, []ScanIssue{}, ctx.Err()
	}

	diagrams := []Diagram{}
	issues := []ScanIssue{}
	var issueMu sync.Mutex
	addIssue := func(path string, err error) {
		issueMu.Lock()
		defer issueMu.Unlock()
		issues = append(issues, ScanIssue{Path: path, Message: err.Error()})
	}
	files := make(chan *gocodewalker.File, 32)
	walker := gocodewalker.NewFileWalker(abs, files)
	walker.IncludeHidden = options.Hidden
	walker.IgnoreGitIgnore = options.NoIgnore
	walker.IgnoreIgnoreFile = options.NoIgnore
	walker.IgnoreGitModules = options.NoIgnore
	// VCS object stores are not source trees, even with --hidden/--no-ignore.
	walker.ExcludeDirectory = []string{".git", ".hg", ".svn"}
	walker.SetErrorHandler(func(err error) bool {
		issuePath := abs
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			issuePath = pathErr.Path
		}
		addIssue(issuePath, err)
		return true
	})
	walkResult := make(chan error, 1)
	go func() { walkResult <- walker.Start() }()
	cancelled := false
	for files != nil {
		select {
		case <-ctx.Done():
			walker.Terminate()
			cancelled = true
			// Drain the producer before returning; the library owns closing this
			// channel and may already be delivering a batch when cancelled.
			for range files {
			}
			files = nil
		case file, ok := <-files:
			if !ok {
				files = nil
				continue
			}
			if !supportedExtension(strings.ToLower(filepath.Ext(file.Location))) {
				continue
			}
			info, err := os.Lstat(file.Location)
			if err != nil {
				addIssue(file.Location, err)
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(file.Location)
			if err != nil {
				addIssue(file.Location, err)
				continue
			}
			doc, err := Parse(file.Location, data)
			if err != nil {
				addIssue(file.Location, err)
				continue
			}
			diagrams = append(diagrams, doc.Diagrams...)
		}
	}
	walkErr := <-walkResult
	sort.Slice(diagrams, func(i, j int) bool {
		if diagrams[i].Path != diagrams[j].Path {
			return diagrams[i].Path < diagrams[j].Path
		}
		return diagrams[i].StartByte < diagrams[j].StartByte
	})
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		return issues[i].Message < issues[j].Message
	})
	if cancelled || ctx.Err() != nil {
		return diagrams, issues, ctx.Err()
	}
	return diagrams, issues, walkErr
}
