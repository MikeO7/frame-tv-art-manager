package samsung

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	operationObserve           = "observe"
	commandUploadName          = "upload"
	commandDeleteName          = "delete"
	commandSelectName          = "select"
	commandConfigureSlidesName = "configure slideshow"
	commandConfigureBrightName = "configure brightness"
	commandWakeName            = "wake"
	commandPowerOffName        = "power off"
	commandUnknownName         = "command"
	fileTypeJPEG               = "jpg"
	fileTypePNG                = "png"
	requestSetSlideshowStatus  = "set_slideshow_status"
)

type preparedCommand struct {
	command Command
	upload  preparedUpload
}

//nolint:gocyclo // The switch exhaustively validates the sealed command sum type.
func prepareCommand(command Command) (preparedCommand, func(), error) {
	prepared := preparedCommand{command: command}
	switch value := command.(type) {
	case Upload:
		upload, cleanup, err := prepareUpload(value)
		prepared.upload = upload
		return prepared, cleanup, err
	case Delete:
		return prepared, nil, validateContentID(value.ContentID)
	case Select:
		return prepared, nil, validateContentID(value.ContentID)
	case ConfigureSlideshow:
		if !value.Previous.Valid() || !value.Desired.Valid() {
			return prepared, nil, errors.New("slideshow settings are invalid")
		}
	case ConfigureBrightness:
		if value.PreviousValue < 0 || value.PreviousValue > 100 || value.Value < 0 || value.Value > 100 {
			return prepared, nil, errors.New("brightness values must be between 0 and 100")
		}
	case Wake, PowerOff:
		// The fresh guard validates their state-specific prerequisites.
	default:
		return prepared, nil, errors.New("unsupported Samsung command")
	}
	return prepared, nil, nil
}

func prepareUpload(command Upload) (preparedUpload, func(), error) {
	fileType, matte, err := validateUploadMetadata(command)
	if err != nil {
		return preparedUpload{}, nil, err
	}
	return openVerifiedUpload(command, fileType, matte)
}

func validateUploadMetadata(command Upload) (string, string, error) {
	if !filepath.IsAbs(command.Path) || filepath.Base(command.Path) != command.Name ||
		filepath.Base(command.Name) != command.Name || strings.TrimSpace(command.Name) == "" {
		return "", "", errors.New("upload path and name are not a committed absolute file binding")
	}
	fileType := strings.ToLower(strings.TrimSpace(command.FileType))
	if fileType != fileTypeJPEG && fileType != fileTypePNG {
		return "", "", errors.New("upload file type is unsupported")
	}
	if command.Size <= 0 || command.Digest == ([sha256.Size]byte{}) {
		return "", "", errors.New("upload size and digest are required")
	}
	matte := strings.TrimSpace(command.Matte)
	if matte == "" || len(matte) > 128 {
		return "", "", errors.New("upload matte is invalid")
	}
	return fileType, matte, nil
}

func openVerifiedUpload(command Upload, fileType, matte string) (preparedUpload, func(), error) {
	pathInfo, err := os.Lstat(command.Path)
	if err != nil {
		return preparedUpload{}, nil, fmt.Errorf("inspect upload: %w", err)
	}
	if !pathInfo.Mode().IsRegular() {
		return preparedUpload{}, nil, errors.New("upload path is not a regular non-symlink file")
	}
	file, err := os.Open(filepath.Clean(command.Path))
	if err != nil {
		return preparedUpload{}, nil, fmt.Errorf("open upload: %w", err)
	}
	cleanup := func() { _ = file.Close() }
	openedInfo, err := file.Stat()
	if err != nil {
		cleanup()
		return preparedUpload{}, nil, fmt.Errorf("inspect opened upload: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) || openedInfo.Size() != command.Size {
		cleanup()
		return preparedUpload{}, nil, errors.New("opened upload does not match committed metadata")
	}
	digest, err := hashOpenFile(file)
	if err != nil {
		cleanup()
		return preparedUpload{}, nil, err
	}
	if digest != command.Digest {
		cleanup()
		return preparedUpload{}, nil, errors.New("upload digest does not match committed metadata")
	}
	return preparedUpload{
		file: file, fileType: fileType, matte: matte, size: command.Size, digest: command.Digest,
	}, cleanup, nil
}

func hashOpenFile(file *os.File) ([sha256.Size]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("rewind upload: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("hash upload: %w", err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func validateContentID(contentID string) error {
	contentID = strings.TrimSpace(contentID)
	if contentID == "" || len(contentID) > 256 {
		return errors.New("content ID is invalid")
	}
	return nil
}

func commandName(command Command) string {
	switch command.(type) {
	case Upload:
		return commandUploadName
	case Delete:
		return commandDeleteName
	case Select:
		return commandSelectName
	case ConfigureSlideshow:
		return commandConfigureSlidesName
	case ConfigureBrightness:
		return commandConfigureBrightName
	case Wake:
		return commandWakeName
	case PowerOff:
		return commandPowerOffName
	default:
		return commandUnknownName
	}
}

func commandError(operation string, outcome Outcome, cause error) *Error {
	kind := ErrorKindNotAuthorized
	retryable := false
	switch outcome {
	case OutcomeUnknown:
		kind = ErrorKindOutcomeUnknown
	case OutcomeNotApplied:
		kind = ErrorKindProtocol
		if errors.Is(cause, ErrStorageFull) {
			kind = ErrorKindStorageFull
		}
	case OutcomeNotAttempted:
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			kind = ErrorKindCanceled
		} else if !errors.Is(cause, ErrNotAuthorized) {
			kind = ErrorKindInvalidResponse
			retryable = true
		}
	case OutcomeApplied:
		kind = ErrorKindNone
	}
	return &Error{Kind: kind, Operation: operation, Retryable: retryable, Outcome: outcome, Cause: cause}
}

func (a *adapter) invalidateAuthorization() {
	a.stateMu.Lock()
	a.connectionGeneration++
	a.stateMu.Unlock()
}

func (a *adapter) publishGuardInventory(inventory Inventory) {
	a.stateMu.Lock()
	a.runtime.InventoryFingerprint = inventory.Fingerprint
	a.stateMu.Unlock()
}

func (a *adapter) finishCommand(receipt Receipt, commandErr error) (Receipt, error) {
	receipt.CompletedAt = a.clock.Now()
	var typed *Error
	if errors.As(commandErr, &typed) {
		receipt.Outcome = typed.Outcome
	}
	return receipt, commandErr
}
