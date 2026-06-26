package agent

import "testing"

func TestExtractFindingReplies(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want []FindingReply
		ok   bool
	}{
		{
			name: "valid array",
			log: `summary stuff
` + FindingsStartMarker + `
[
  {"finding": 1, "addressed": true, "reply": "Narrowed the regex."},
  {"finding": 2, "addressed": false, "reply": "Kept by design."}
]
` + FindingsEndMarker,
			want: []FindingReply{
				{Finding: 1, Addressed: true, Reply: "Narrowed the regex."},
				{Finding: 2, Addressed: false, Reply: "Kept by design."},
			},
			ok: true,
		},
		{
			name: "no markers",
			log:  "just a prose summary, no findings block",
			ok:   false,
		},
		{
			name: "malformed json falls back",
			log:  FindingsStartMarker + "\n[not json}\n" + FindingsEndMarker,
			ok:   false,
		},
		{
			name: "empty array",
			log:  FindingsStartMarker + "\n[]\n" + FindingsEndMarker,
			ok:   false,
		},
		{
			name: "drops empty reply and non-positive finding",
			log: FindingsStartMarker + `
[
  {"finding": 0, "addressed": true, "reply": "ignored"},
  {"finding": 1, "addressed": true, "reply": "   "},
  {"finding": 2, "addressed": true, "reply": "kept"}
]
` + FindingsEndMarker,
			want: []FindingReply{{Finding: 2, Addressed: true, Reply: "kept"}},
			ok:   true,
		},
		{
			name: "duplicate finding last wins",
			log: FindingsStartMarker + `
[
  {"finding": 1, "addressed": false, "reply": "first"},
  {"finding": 1, "addressed": true, "reply": "second"}
]
` + FindingsEndMarker,
			want: []FindingReply{{Finding: 1, Addressed: true, Reply: "second"}},
			ok:   true,
		},
		{
			name: "scoped to last attempt",
			log: `--- Attempt 1 ---
` + FindingsStartMarker + "\n[{\"finding\":1,\"addressed\":true,\"reply\":\"stale\"}]\n" + FindingsEndMarker + `
--- Attempt 2 ---
` + FindingsStartMarker + "\n[{\"finding\":1,\"addressed\":false,\"reply\":\"fresh\"}]\n" + FindingsEndMarker,
			want: []FindingReply{{Finding: 1, Addressed: false, Reply: "fresh"}},
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractFindingReplies(tt.log)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tt.ok, got)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d replies, want %d: %v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("reply %d = %+v, want %+v", i, got[i], w)
				}
			}
		})
	}
}
