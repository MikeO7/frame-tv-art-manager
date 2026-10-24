package samsung

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandPolicyCoversEveryCommandAndFailureOutcome(t *testing.T) {
	commands := []struct {
		command Command
		name    string
	}{
		{command: Upload{}, name: commandUploadName},
		{command: Delete{ContentID: "art-1"}, name: commandDeleteName},
		{command: Select{ContentID: "art-1"}, name: commandSelectName},
		{command: ConfigureSlideshow{
			Previous: SlideshowSetting{Interval: 15, Kind: SlideshowShuffle},
			Desired:  SlideshowSetting{Interval: 30, Kind: SlideshowSequential},
		}, name: commandConfigureSlidesName},
		{command: ConfigureBrightness{PreviousValue: 25, Value: 50}, name: commandConfigureBrightName},
		{command: Wake{}, name: commandWakeName},
		{command: PowerOff{}, name: commandPowerOffName},
		{command: nil, name: commandUnknownName},
	}
	for _, test := range commands {
		if got := commandName(test.command); got != test.name {
			t.Errorf("commandName(%T) = %q, want %q", test.command, got, test.name)
		}
	}

	tests := []struct {
		name      string
		outcome   Outcome
		cause     error
		wantKind  ErrorKind
		retryable bool
	}{
		{name: "unknown", outcome: OutcomeUnknown, cause: io.ErrUnexpectedEOF, wantKind: ErrorKindOutcomeUnknown},
		{name: "protocol rejection", outcome: OutcomeNotApplied, cause: ErrArtAPIError, wantKind: ErrorKindProtocol},
		{name: "storage full", outcome: OutcomeNotApplied, cause: ErrStorageFull, wantKind: ErrorKindStorageFull},
		{name: "canceled before write", outcome: OutcomeNotAttempted, cause: context.Canceled, wantKind: ErrorKindCanceled},
		{name: "unauthorized before write", outcome: OutcomeNotAttempted, cause: ErrNotAuthorized, wantKind: ErrorKindNotAuthorized},
		{name: "invalid command before write", outcome: OutcomeNotAttempted, cause: errors.New("bad command"), wantKind: ErrorKindInvalidResponse, retryable: true},
		{name: "success", outcome: OutcomeApplied, wantKind: ErrorKindNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := commandError("apply", test.outcome, test.cause)
			if got.Kind != test.wantKind || got.Retryable != test.retryable || got.Outcome != test.outcome {
				t.Fatalf("commandError() = %+v", got)
			}
		})
	}
}

func TestPrepareCommandRejectsUnboundOrUnsafeInputs(t *testing.T) {
	nonzeroDigest := sha256.Sum256([]byte("art"))
	absentPath := filepath.Join(t.TempDir(), "absent.jpg")
	longID := strings.Repeat("x", 257)
	jpegType := fileTypeJPEG
	tests := []struct {
		name    string
		command Command
	}{
		{name: "nil command", command: nil},
		{name: "blank delete ID", command: Delete{}},
		{name: "oversized select ID", command: Select{ContentID: longID}},
		{name: "invalid slideshow previous", command: ConfigureSlideshow{
			Previous: SlideshowSetting{Interval: -1, Kind: SlideshowSequential},
			Desired:  SlideshowSetting{Interval: 1, Kind: SlideshowSequential},
		}},
		{name: "invalid slideshow desired", command: ConfigureSlideshow{
			Previous: SlideshowSetting{Interval: 1, Kind: SlideshowSequential},
			Desired:  SlideshowSetting{Interval: -1, Kind: SlideshowShuffle},
		}},
		{name: "brightness previous too high", command: ConfigureBrightness{PreviousValue: 101}},
		{name: "brightness value too high", command: ConfigureBrightness{Value: 101}},
		{name: "relative upload path", command: Upload{Path: "art.jpg", Name: "art.jpg", FileType: jpegType, Matte: "none", Size: 3, Digest: nonzeroDigest}},
		{name: "upload name mismatch", command: Upload{Path: absentPath, Name: "other.jpg", FileType: jpegType, Matte: "none", Size: 3, Digest: nonzeroDigest}},
		{name: "unsupported upload type", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: "gif", Matte: "none", Size: 3, Digest: nonzeroDigest}},
		{name: "missing upload size", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: jpegType, Matte: "none", Digest: nonzeroDigest}},
		{name: "missing upload digest", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: jpegType, Matte: "none", Size: 3}},
		{name: "blank matte", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: jpegType, Size: 3, Digest: nonzeroDigest}},
		{name: "oversized matte", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: jpegType, Matte: strings.Repeat("m", 129), Size: 3, Digest: nonzeroDigest}},
		{name: "missing bound file", command: Upload{Path: absentPath, Name: "absent.jpg", FileType: jpegType, Matte: "none", Size: 3, Digest: nonzeroDigest}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, cleanup, err := prepareCommand(test.command)
			if cleanup != nil {
				cleanup()
			}
			if err == nil {
				t.Fatalf("prepareCommand(%T) error = nil", test.command)
			}
		})
	}

	for _, command := range []Command{
		Delete{ContentID: " id "},
		Select{ContentID: "id"},
		ConfigureSlideshow{
			Previous: SlideshowSetting{Kind: SlideshowShuffle},
			Desired:  SlideshowSetting{Interval: 60, Kind: SlideshowSequential},
		},
		ConfigureBrightness{PreviousValue: 0, Value: 100},
		Wake{},
		PowerOff{},
	} {
		_, cleanup, err := prepareCommand(command)
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			t.Errorf("prepareCommand(%T) error = %v", command, err)
		}
	}
}
