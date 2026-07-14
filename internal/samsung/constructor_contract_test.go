package samsung

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNewAdapterBuildsProductionTransportWithSafeDefaults(t *testing.T) {
	config := validAdapterConfig(t)
	before := time.Now()
	adapter, err := NewAdapter(config, Dependencies{})
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if adapter.clock == nil || adapter.random == nil || adapter.logger == nil || adapter.transport == nil {
		t.Fatalf("NewAdapter() omitted defaults: %+v", adapter)
	}
	if got := adapter.clock.Now(); got.Before(before) || got.After(time.Now()) {
		t.Fatalf("default clock returned implausible time %v", got)
	}
	if _, ok := adapter.transport.(*protocolTransport); !ok {
		t.Fatalf("production transport = %T, want *protocolTransport", adapter.transport)
	}
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close() before connection error = %v", err)
	}
}

func TestAdapterConfigValidationRejectsEveryUnsafeBoundary(t *testing.T) {
	valid := validAdapterConfig(t)
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "address", mutate: func(config *Config) { config.Address = " " }},
		{name: "client name", mutate: func(config *Config) { config.ClientName = " " }},
		{name: "token path", mutate: func(config *Config) { config.TokenPath = " " }},
		{name: "connect timeout", mutate: func(config *Config) { config.ConnectTimeout = 0 }},
		{name: "request timeout", mutate: func(config *Config) { config.RequestTimeout = 0 }},
		{name: "gate timeout", mutate: func(config *Config) { config.GateTimeout = 0 }},
		{name: "backoff base", mutate: func(config *Config) { config.BackoffBase = 0 }},
		{name: "backoff maximum", mutate: func(config *Config) { config.BackoffMaximum = 0 }},
		{name: "backoff above limit", mutate: func(config *Config) { config.BackoffMaximum = maxBackoffDelay + time.Second }},
		{name: "backoff order", mutate: func(config *Config) { config.BackoffBase = 2 * time.Minute; config.BackoffMaximum = time.Minute }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewAdapter(config, Dependencies{}); err == nil {
				t.Fatal("NewAdapter() accepted unsafe configuration")
			}
		})
	}
}

func TestCommandNamesAndValidationCoverSealedCommandSet(t *testing.T) {
	tests := []struct {
		command Command
		name    string
	}{
		{command: Delete{ContentID: "id"}, name: "delete"},
		{command: Select{ContentID: "id"}, name: "select"},
		{command: ConfigureSlideshow{
			Previous: SlideshowSetting{Interval: 1, Kind: SlideshowShuffle},
			Desired:  SlideshowSetting{Interval: 2, Kind: SlideshowSequential},
		}, name: "configure slideshow"},
		{command: ConfigureBrightness{PreviousValue: 1, Value: 2}, name: "configure brightness"},
		{command: Wake{}, name: "wake"},
		{command: PowerOff{}, name: "power off"},
	}
	for _, test := range tests {
		if got := commandName(test.command); got != test.name {
			t.Errorf("commandName(%T) = %q, want %q", test.command, got, test.name)
		}
		prepared, cleanup, err := prepareCommand(test.command)
		if cleanup != nil {
			cleanup()
		}
		if err != nil || prepared.command != test.command {
			t.Errorf("prepareCommand(%T) = %+v, %v", test.command, prepared, err)
		}
	}

	invalid := []Command{
		Delete{},
		Select{ContentID: string(make([]byte, 257))},
		ConfigureSlideshow{Previous: SlideshowSetting{Interval: -1, Kind: SlideshowSequential}},
		ConfigureBrightness{PreviousValue: 101},
	}
	for _, command := range invalid {
		if _, cleanup, err := prepareCommand(command); err == nil {
			if cleanup != nil {
				cleanup()
			}
			t.Errorf("prepareCommand(%T) accepted invalid input", command)
		}
	}

	canceled := commandError("wake", OutcomeNotAttempted, context.Canceled)
	if !errors.Is(canceled, context.Canceled) || canceled.Kind != ErrorKindCanceled || canceled.Retryable {
		t.Fatalf("canceled command classification = %+v", canceled)
	}
}

func validAdapterConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Address:        "192.0.2.10",
		ClientName:     "test-client",
		TokenPath:      filepath.Join(t.TempDir(), "token.txt"),
		ConnectTimeout: time.Second,
		RequestTimeout: time.Second,
		GateTimeout:    time.Second,
		BackoffBase:    time.Second,
		BackoffMaximum: time.Minute,
	}
}
