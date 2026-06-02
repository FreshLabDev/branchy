// SPDX-License-Identifier: Apache-2.0
package webhooks

import "testing"

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ok":true}`)
	secret := "top-secret"
	valid := SignForTest(secret, body)

	tests := []struct {
		name      string
		secret    string
		body      []byte
		signature string
		want      bool
	}{
		{name: "valid", secret: secret, body: body, signature: valid, want: true},
		{name: "invalid secret", secret: "other", body: body, signature: valid, want: false},
		{name: "changed body", secret: secret, body: []byte(`{"ok":false}`), signature: valid, want: false},
		{name: "missing", secret: secret, body: body, signature: "", want: false},
		{name: "wrong prefix", secret: secret, body: body, signature: "sha1=abc", want: false},
		{name: "malformed hex", secret: secret, body: body, signature: "sha256=not-hex", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifySignature(tt.secret, tt.body, tt.signature)
			if got != tt.want {
				t.Fatalf("VerifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
