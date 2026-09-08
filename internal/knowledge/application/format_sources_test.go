package application

import (
	"strings"
	"testing"
)

func TestFormatSources(t *testing.T) {
	cases := []struct {
		name    string
		sources []Source
		want    string
	}{
		{
			name:    "empty sources format to empty string",
			sources: nil,
			want:    "",
		},
		{
			name:    "leaf without parent formats content only",
			sources: []Source{{Content: "leaf text"}},
			want:    "leaf text\n---\n",
		},
		{
			name: "leaf with parent appends parent after content",
			sources: []Source{
				{Content: "leaf text", ParentContent: "whole section text"},
			},
			want: "leaf text\n\nwhole section text\n---\n",
		},
		{
			name: "multiple leaves share one parent but parent appears once",
			sources: []Source{
				{Content: "leaf 1", ParentContent: "shared section"},
				{Content: "leaf 2", ParentContent: "shared section"},
			},
			want: "leaf 1\n\nshared section\n---\nleaf 2\n---\n",
		},
		{
			name: "leaves in distinct parents each carry their own parent",
			sources: []Source{
				{Content: "leaf 1", ParentContent: "section A"},
				{Content: "leaf 2", ParentContent: "section B"},
			},
			want: "leaf 1\n\nsection A\n---\nleaf 2\n\nsection B\n---\n",
		},
		{
			name: "empty parent does not emit a blank parent block",
			sources: []Source{
				{Content: "leaf A"},
				{Content: "leaf B", ParentContent: "section B"},
			},
			want: "leaf A\n---\nleaf B\n\nsection B\n---\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSources(tc.sources)
			if got != tc.want {
				t.Errorf("formatSources() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatSourcesOutputHasBlockSeparators(t *testing.T) {
	got := formatSources([]Source{{Content: "a"}, {Content: "b"}})
	if strings.Count(got, "---\n") != 2 {
		t.Errorf("formatSources() separators = %d, want 2; output=%q", strings.Count(got, "---\n"), got)
	}
}
