package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func copyStageInput(ctx context.Context, directory string, input StageInput) (err error) {
	source, err := openStageInput(input)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.file.Close()) }()
	return writeStageInput(ctx, directory, input, source)
}

type stageSource struct {
	file           *os.File
	before, opened os.FileInfo
}

func openStageInput(input StageInput) (stageSource, error) {
	before, err := os.Lstat(input.Path)
	if err != nil {
		return stageSource{}, fmt.Errorf("inspect stage input %q: %w", input.Name, err)
	}
	if err := requireRegularStageInput(input.Name, before); err != nil {
		return stageSource{}, err
	}
	source, err := os.Open(input.Path)
	if err != nil {
		return stageSource{}, fmt.Errorf("open stage input %q: %w", input.Name, err)
	}
	opened, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return stageSource{}, fmt.Errorf("inspect opened stage input %q: %w", input.Name, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = source.Close()
		return stageSource{}, fmt.Errorf("stage input %q changed while opening", input.Name)
	}
	return stageSource{file: source, before: before, opened: opened}, nil
}

func writeStageInput(
	ctx context.Context,
	directory string,
	input StageInput,
	source stageSource,
) (err error) {
	destinationPath := filepath.Join(directory, input.Name)
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged input %q: %w", input.Name, err)
	}
	destinationOpen := true
	defer func() {
		if destinationOpen {
			err = errors.Join(err, destination.Close())
		}
	}()

	hash := sha256.New()
	reader := &contextReader{ctx: ctx, reader: source.file}
	if _, err := io.Copy(io.MultiWriter(destination, hash), reader); err != nil {
		return fmt.Errorf("copy stage input %q: %w", input.Name, err)
	}
	if got := [sha256.Size]byte(hash.Sum(nil)); got != input.Digest {
		return fmt.Errorf("stage input %q digest changed: got %x, want %x", input.Name, got, input.Digest)
	}
	if err := verifyStageInputStable(input, source.file, source.before, source.opened); err != nil {
		return err
	}
	if err := destination.Chmod(0o644); err != nil {
		return fmt.Errorf("set staged input mode %q: %w", input.Name, err)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync staged input %q: %w", input.Name, err)
	}
	if err := destination.Close(); err != nil {
		destinationOpen = false
		return fmt.Errorf("close staged input %q: %w", input.Name, err)
	}
	destinationOpen = false
	return nil
}

func requireRegularStageInput(name string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("stage input %q is not a regular non-symlink file", name)
	}
	return nil
}

func verifyStageInputStable(input StageInput, source *os.File, before, opened os.FileInfo) error {
	after, err := source.Stat()
	if err != nil {
		return fmt.Errorf("reinspect opened stage input %q: %w", input.Name, err)
	}
	pathAfter, err := os.Lstat(input.Path)
	if err != nil {
		return fmt.Errorf("reinspect stage input path %q: %w", input.Name, err)
	}
	if err := requireRegularStageInput(input.Name, pathAfter); err != nil {
		return err
	}
	if !os.SameFile(before, opened) || !os.SameFile(opened, after) || !os.SameFile(after, pathAfter) {
		return fmt.Errorf("stage input %q changed identity while copying", input.Name)
	}
	if opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("stage input %q changed while copying", input.Name)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
