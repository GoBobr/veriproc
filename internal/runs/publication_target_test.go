package runs

import "testing"

func TestCleanPublicationSubpath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "root default", input: "", want: ""},
		{name: "normalizes relative", input: "validated/../validated/scene-l2", want: "validated/scene-l2"},
		{name: "rejects absolute", input: "/tmp/out", wantErr: true},
		{name: "rejects escape", input: "../outside", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanPublicationSubpath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("cleanPublicationSubpath = %q, want %q", got, tt.want)
			}
		})
	}
}
