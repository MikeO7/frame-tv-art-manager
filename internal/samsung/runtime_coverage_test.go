package samsung

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyObservationErrorPolicy(t *testing.T) {
	operation := operationObserve
	tests := []struct {
		name      string
		cause     error
		wantKind  ErrorKind
		retryable bool
	}{
		{name: "canceled", cause: context.Canceled, wantKind: ErrorKindCanceled},
		{name: "deadline", cause: context.DeadlineExceeded, wantKind: ErrorKindCanceled},
		{name: "unauthorized", cause: ErrUnauthorized, wantKind: ErrorKindUnauthorized},
		{name: "timeout", cause: ErrTimeout, wantKind: ErrorKindTimeout, retryable: true},
		{name: "connection failure", cause: ErrConnectionFailure, wantKind: ErrorKindUnreachable, retryable: true},
		{name: "not connected", cause: ErrNotConnected, wantKind: ErrorKindUnreachable, retryable: true},
		{name: "invalid response", cause: errors.New("malformed response"), wantKind: ErrorKindInvalidResponse, retryable: true},
		{
			name: "typed policy is preserved",
			cause: &Error{
				Kind:      ErrorKindPersistence,
				Operation: "persist token",
				Retryable: false,
				Cause:     io.ErrUnexpectedEOF,
			},
			wantKind: ErrorKindPersistence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyObservationError(operation, test.cause)
			if got.Kind != test.wantKind || got.Retryable != test.retryable {
				t.Fatalf("classifyObservationError() = %+v, want kind %v retryable %t", got, test.wantKind, test.retryable)
			}
			if got.Operation != operation || got.Outcome != OutcomeNotAttempted || !errors.Is(got, test.cause) {
				t.Fatalf("classification lost operation, outcome, or cause: %+v", got)
			}
		})
	}
}

func TestBackoffDelayIsBoundedAndHandlesEntropyFailure(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		random   io.Reader
		want     time.Duration
	}{
		{name: "minimum jitter", failures: 1, random: bytes.NewReader(make([]byte, 8)), want: 0},
		{name: "maximum jitter", failures: 1, random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 8)), want: 10 * time.Second},
		{name: "exponential maximum", failures: 20, random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 8)), want: 40 * time.Second},
		{name: "entropy failure fails to upper bound", failures: 2, random: bytes.NewReader(nil), want: 20 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &adapter{
				config: Config{BackoffBase: 10 * time.Second, BackoffMaximum: 40 * time.Second},
				random: test.random,
				logger: slog.New(slog.DiscardHandler),
			}
			if got := adapter.backoffDelay(test.failures); got != test.want {
				t.Fatalf("backoffDelay(%d) = %v, want %v", test.failures, got, test.want)
			}
		})
	}
}

func TestAdapterCloseContextRetainsCleanupOpportunity(t *testing.T) {
	active := context.Background()
	activeClose, activeCancel := adapterCloseContext(active, time.Second)
	defer activeCancel()
	if activeClose != active {
		t.Fatal("active context was unnecessarily replaced")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cleanup, cleanupCancel := adapterCloseContext(canceled, time.Second)
	defer cleanupCancel()
	if cleanup.Err() != nil {
		t.Fatalf("cleanup context inherited cancellation: %v", cleanup.Err())
	}
	if _, ok := cleanup.Deadline(); !ok {
		t.Fatal("cleanup context has no bounded deadline")
	}
}

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

func TestWakeOnLANValidationHonorsCancellation(t *testing.T) {
	if err := sendWakeOnLAN(context.Background(), net.HardwareAddr{1, 2, 3}, time.Second); err == nil {
		t.Fatal("short MAC address accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := sendWakeOnLAN(canceled, net.HardwareAddr{0, 1, 2, 3, 4, 5}, time.Second)
	if err == nil {
		t.Fatal("Wake-on-LAN ignored canceled context")
	}
}

func TestProtocolStringDecodesStringsAndScalarsAndRejectsStructuredValues(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    protocolString
		wantErr bool
	}{
		{name: "string", raw: `"on"`, want: "on"},
		{name: "number", raw: `42`, want: "42"},
		{name: "boolean", raw: `true`, want: "true"},
		{name: "empty", raw: ``, wantErr: true},
		{name: "null uses zero string", raw: `null`},
		{name: "object", raw: `{}`, wantErr: true},
		{name: "array", raw: `[]`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got protocolString
			err := got.UnmarshalJSON([]byte(test.raw))
			if (err != nil) != test.wantErr {
				t.Fatalf("UnmarshalJSON(%q) error = %v, wantErr %t", test.raw, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("UnmarshalJSON(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestRequestIDFallbackRemainsUniqueWhenEntropyFails(t *testing.T) {
	first := requestIDFrom(errorReader{})
	second := requestIDFrom(errorReader{})
	if first == second || len(first) != 36 || len(second) != 36 {
		t.Fatalf("fallback IDs = %q, %q", first, second)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSamsungErrorNilSafetyAndWrapping(t *testing.T) {
	var nilError *Error
	if nilError.Error() != "<nil>" || nilError.Unwrap() != nil {
		t.Fatal("nil Samsung error methods are not safe")
	}

	operation := operationObserve
	withoutCause := &Error{Operation: operation}
	if got, want := withoutCause.Error(), "samsung "+operation+" failed"; got != want {
		t.Fatalf("Error() = %q", got)
	}
	cause := errors.New("wire failure")
	withCause := &Error{Operation: "apply", Cause: cause}
	if !strings.Contains(withCause.Error(), cause.Error()) || !errors.Is(withCause, cause) {
		t.Fatalf("Samsung error did not preserve cause: %v", withCause)
	}
}
