package samsung

import (
	"encoding/json"
	"testing"
)

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

func TestParsePolyStringVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "empty"},
		{name: "null", raw: json.RawMessage("null")},
		{name: "escaped string", raw: json.RawMessage(`"{\"id\":1}"`), want: `{"id":1}`},
		{name: "object", raw: json.RawMessage(`{"id":1}`), want: `{"id":1}`},
		{name: "array", raw: json.RawMessage(`[1,2]`), want: `[1,2]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePolyString(tt.raw); got != tt.want {
				t.Fatalf("parsePolyString() = %q, want %q", got, tt.want)
			}
		})
	}
}
