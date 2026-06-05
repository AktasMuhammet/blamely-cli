package gitnotes

import (
	"testing"

	"github.com/blamely/blamely/internal/config"
)

func roles(turns []ConvTurn) []string {
	out := make([]string, len(turns))
	for i, t := range turns {
		out[i] = t.Role
	}
	return out
}

func TestFilterConversation(t *testing.T) {
	turns := []ConvTurn{
		{Role: "user", Text: "do X"},
		{Role: "assistant", Text: "done X"},
		{Role: "User", Text: "now Y"}, // mixed case must still match
	}

	cases := []struct {
		name string
		nc   config.NoteConfig
		want []string
	}{
		{"both on", config.NoteConfig{Conversation: true, ConversationUser: true, ConversationAssistant: true},
			[]string{"user", "assistant", "User"}},
		{"master off drops all", config.NoteConfig{Conversation: false, ConversationUser: true, ConversationAssistant: true},
			nil},
		{"assistant only", config.NoteConfig{Conversation: true, ConversationUser: false, ConversationAssistant: true},
			[]string{"assistant"}},
		{"user only", config.NoteConfig{Conversation: true, ConversationUser: true, ConversationAssistant: false},
			[]string{"user", "User"}},
		{"both roles off → nil", config.NoteConfig{Conversation: true, ConversationUser: false, ConversationAssistant: false},
			nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterConversation(turns, tc.nc)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("want nil, got %v", roles(got))
				}
				return
			}
			gr := roles(got)
			if len(gr) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, gr)
			}
			for i := range gr {
				if gr[i] != tc.want[i] {
					t.Fatalf("want %v, got %v", tc.want, gr)
				}
			}
		})
	}
}
