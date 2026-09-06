package application

import "testing"

func TestApplySourceTitles(t *testing.T) {
	cases := []struct {
		name    string
		sources []Source
		titles  map[string]string
		want    []string // 每个 source 期望的 DocumentTitle
	}{
		{
			name: "docID 命中则回填源文件名，未命中保持空",
			sources: []Source{
				{DocumentID: "doc-1"},
				{DocumentID: "doc-2"},
				{DocumentID: "missing"},
			},
			titles: map[string]string{"doc-1": "用户手册.pdf", "doc-2": "q3-report.md"},
			want:   []string{"用户手册.pdf", "q3-report.md", ""},
		},
		{
			name:    "空 titles 不改动任何 source",
			sources: []Source{{DocumentID: "doc-1"}},
			titles:  map[string]string{},
			want:    []string{""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applySourceTitles(tc.sources, tc.titles)
			for i, src := range tc.sources {
				if src.DocumentTitle != tc.want[i] {
					t.Fatalf("source %d: want title %q, got %q", i, tc.want[i], src.DocumentTitle)
				}
			}
		})
	}
}
