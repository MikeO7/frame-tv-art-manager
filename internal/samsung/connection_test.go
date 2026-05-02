package samsung

import (
	"encoding/json"
	"testing"
)

func TestArtAppRequest(t *testing.T) {
	data := map[string]any{"hello": "world"}
	b, err := ArtAppRequest(data)
	if err != nil {
		t.Fatal(err)
	}

	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Event string `json:"event"`
			Data  string `json:"data"`
		} `json:"params"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.Method != "ms.channel.emit" {
		t.Errorf("expected ms.channel.emit, got %s", envelope.Method)
	}
	if envelope.Params.Event != "art_app_request" {
		t.Errorf("expected art_app_request, got %s", envelope.Params.Event)
	}
	if envelope.Params.Data != `{"hello":"world"}` {
		t.Errorf("expected inner data, got %s", envelope.Params.Data)
	}
}

func TestNewRequestID(t *testing.T) {
	id1 := NewRequestID()
	id2 := NewRequestID()
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	if len(id1) < 30 {
		t.Errorf("expected UUID-like string, got %s", id1)
	}
}
