package durablefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

type stableReadOperations struct {
	lstat func(string) (fs.FileInfo, error)
	open  func(string) (*os.File, error)
	close func(*os.File) error
}

// StableReadOptions defines the trust boundary for one control-file read.
// RequiredMode is ignored when zero.
type StableReadOptions struct {
	MaxBytes     int64
	RequiredMode fs.FileMode
}

func defaultStableReadOperations() stableReadOperations {
	return stableReadOperations{
		lstat: os.Lstat,
		open:  os.Open,
		close: func(file *os.File) error { return file.Close() },
	}
}

// ReadStable reads a bounded regular file only when the pathname continues to
// identify the same unchanged, non-symlink file for the complete operation.
func ReadStable(ctx context.Context, path string, options StableReadOptions) ([]byte, error) {
	return readStable(ctx, path, options, defaultStableReadOperations())
}

func readStable(
	ctx context.Context,
	path string,
	options StableReadOptions,
	ops stableReadOperations,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("stable read context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read stable file %s before work: %w", path, err)
	}
	if options.MaxBytes <= 0 {
		return nil, errors.New("stable read byte limit must be positive")
	}

	before, err := ops.lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect stable file %s: %w", path, err)
	}
	if err := validateStableFile(path, before, options); err != nil {
		return nil, err
	}

	file, err := ops.open(path)
	if err != nil {
		return nil, fmt.Errorf("open stable file %s: %w", path, err)
	}
	opened := openedStableFile{path: path, options: options, before: before, file: file, ops: ops}
	data, operationErr := opened.read(ctx)
	closeErr := ops.close(file)
	if closeErr != nil {
		closeErr = fmt.Errorf("close stable file %s: %w", path, closeErr)
	}
	if err := errors.Join(operationErr, closeErr); err != nil {
		return nil, err
	}
	return data, nil
}

type openedStableFile struct {
	path    string
	options StableReadOptions
	before  fs.FileInfo
	file    *os.File
	ops     stableReadOperations
}

func (read openedStableFile) read(ctx context.Context) ([]byte, error) {
	opened, err := read.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened stable file %s: %w", read.path, err)
	}
	if err := validateStableFile(read.path, opened, read.options); err != nil {
		return nil, err
	}
	if !sameStableFile(read.before, opened) {
		return nil, fmt.Errorf("stable file %s changed while opening", read.path)
	}

	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: read.file}, read.options.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read stable file %s: %w", read.path, err)
	}
	if int64(len(data)) > read.options.MaxBytes {
		return nil, fmt.Errorf("stable file %s exceeds %d-byte limit", read.path, read.options.MaxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read stable file %s: %w", read.path, err)
	}

	after, err := read.ops.lstat(read.path)
	if err != nil {
		return nil, fmt.Errorf("reinspect stable file %s: %w", read.path, err)
	}
	if err := validateStableFile(read.path, after, read.options); err != nil {
		return nil, err
	}
	if !sameStableFile(read.before, opened) || !sameStableFile(opened, after) {
		return nil, fmt.Errorf("stable file %s changed while reading", read.path)
	}
	return data, nil
}

func validateStableFile(path string, info fs.FileInfo, options StableReadOptions) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("stable file %s must be a regular non-symlink file", path)
	}
	if options.RequiredMode != 0 && info.Mode().Perm() != options.RequiredMode.Perm() {
		return fmt.Errorf(
			"stable file %s mode is %04o, want %04o",
			path,
			info.Mode().Perm(),
			options.RequiredMode.Perm(),
		)
	}
	if info.Size() > options.MaxBytes {
		return fmt.Errorf("stable file %s exceeds %d-byte limit", path, options.MaxBytes)
	}
	return nil
}

func sameStableFile(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
