package audit

import (
	"errors"
	"os"
	"sync"
)

// All in-process writers share filesystem coordination, not just one writer's
// statistics mutex. Never hold this lock across database replay calls.
var spoolFiles sync.Mutex
var spoolReplay sync.Mutex

func openSpoolSnapshot(path string) (*os.File, os.FileInfo, error) {
	spoolFiles.Lock()
	defer spoolFiles.Unlock()
	file, err := os.Open(path) // #nosec G304 -- operator-configured spool path
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	return file, info, nil
}

func removeUnchangedSpool(path string, snapshot os.FileInfo) error {
	spoolFiles.Lock()
	defer spoolFiles.Unlock()
	current, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Appends after the snapshot remain for a later idempotent replay. Removing
	// a changed journal would discard evidence that this replay never read.
	if !os.SameFile(snapshot, current) || snapshot.Size() != current.Size() || !snapshot.ModTime().Equal(current.ModTime()) {
		return nil
	}
	return os.Remove(path)
}
