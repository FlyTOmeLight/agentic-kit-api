package contextbuilder

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildAttachmentProjectionAndUsage(t *testing.T) {
	image := Attachment{
		Kind:      "image",
		URI:       "https://media.test/image",
		Name:      "图",
		MediaType: "image/jpeg",
		SizeBytes: 50 << 20,
	}
	video := Attachment{
		Kind:      "video",
		URI:       "https://media.test/video",
		Name:      "video",
		MediaType: "video/mp4",
		SizeBytes: 20 << 20,
	}
	doc := Attachment{
		Kind:      "document",
		URI:       "artifact://tenant/version/manual",
		Name:      "manual",
		MediaType: "application/pdf",
	}
	attachments := []Attachment{image, video, doc}
	for _, tc := range []struct {
		name       string
		modalities []string
		direct     []Attachment
		metadata   []Attachment
		extra      int64
	}{
		{name: "default", modalities: nil, direct: []Attachment{image}, metadata: []Attachment{video, doc}, extra: 1024},
		{name: "video enabled", modalities: []string{"video", "image"}, direct: []Attachment{image, video}, metadata: []Attachment{doc}, extra: 3072},
		{name: "text only", modalities: []string{"text"}, direct: nil, metadata: attachments, extra: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter, err := ResolveCounter(CJKWeightedCounterVersion)
			if err != nil {
				t.Fatal(err)
			}
			in := BuildInput{
				FrozenMaterials: FrozenMaterials{
					History: []MessageGroup{
						historyGroup(
							"old",
							Message{
								Role:        RoleUser,
								Content:     "old",
								Attachments: attachments,
							},
						),
					},
				},
				CurrentTurn: []Message{
					{Role: RoleUser, Content: "describe", Attachments: attachments},
				},
				Policy: Policy{
					Version: PolicyVersionV4,
				},
				Counter:          counter,
				DirectModalities: tc.modalities,
			}
			got := mustBuild(t, in)
			if len(got.Messages) != 3 || got.Messages[0].Attachments != nil ||
				got.Messages[0].Content != "old\n\n"+AttachmentSummaryText(attachments) ||
				!strings.Contains(got.Messages[1].Content, `label="files"`) ||
				got.Messages[2].Content != "describe\n\n"+FormatAttachmentsMetadata(tc.metadata) ||
				!slices.Equal(got.Messages[2].Attachments, tc.direct) {
				t.Fatalf("projection = %+v", got.Messages)
			}
			var bytes, tokens int64
			for _, message := range got.Messages {
				m := measureText(counter, messageText(message))
				bytes += m.bytes
				tokens += m.tokens
			}
			for _, a := range tc.direct {
				bytes += int64(len(a.URI))
			}
			if got.Usage.InputBytes != bytes || got.Usage.InputTokens != tokens+tc.extra {
				t.Fatalf("usage = %+v, want bytes=%d tokens=%d", got.Usage, bytes, tokens+tc.extra)
			}
			if in.CurrentTurn[0].Content != "describe" ||
				in.FrozenMaterials.History[0].Messages[0].Content != "old" ||
				!slices.Equal(in.CurrentTurn[0].Attachments, attachments) {
				t.Fatalf("mutated input: %+v", in)
			}
		})
	}
	for _, uri := range []string{
		"/workspace/image.png",
		"artifact://tenant/version/image.png",
		"//bad/image",
		"https://user@host/image",
		"https://host/image#fragment",
		"http://assets.example/a.png",
		"https://localhost/a.png",
		"https://sub.localhost/a.png",
		"https://service.local/a.png",
		"https://metadata.google.internal/a.png",
		"https://127.0.0.1/a.png",
		"https://10.0.0.5/a.png",
		"https://169.254.169.254/a.png",
		"https://100.64.0.1/a.png",
		"https://[::ffff:127.0.0.1]/a.png",
		"https://2130706433/a.png",
		"https://0x7f000001/a.png",
	} {
		a := image
		a.URI = uri
		if IsDirectMediaAttachment(a, nil) {
			t.Fatalf("direct media URI accepted: %q", uri)
		}
	}
	validImage := image
	validImage.URI = "https://assets.example/a.png"
	if !IsDirectMediaAttachment(validImage, nil) {
		t.Fatalf("public HTTPS direct media URI rejected: %q", validImage.URI)
	}
}

func TestRenderFileIndexBoundsAndNewestWins(t *testing.T) {
	frozen := []FileIndexEntry{
		{URI: "/file", Name: "old", Source: "workspace", Action: "written"},
		{URI: "/file", Name: "attachment", Source: "attachment", Action: "uploaded"},
		{URI: "/history", Source: "workspace", Action: "written"},
	}
	live := []FileIndexEntry{{URI: "/file", Name: "new", Source: "workspace", Action: "edited", Accessible: true}}
	block, ok := RenderFileIndex(frozen, live)
	if !ok || block.Label != "files" || !strings.HasPrefix(block.Content, `[workspace/accessible] edited "new"`) ||
		!strings.Contains(
			block.Content,
			"[workspace/historical]",
		) || !strings.Contains(block.Content, "[attachment]") ||
		strings.Contains(block.Content, `"old"`) || strings.Count(block.Content, "\n") != 2 {
		t.Fatalf("file index = %+v", block)
	}
	for i := range 80 {
		live = append(
			live,
			FileIndexEntry{URI: fmt.Sprintf("/%02d", i), Source: "workspace", Action: "written"},
		)
	}
	block, _ = RenderFileIndex(nil, live)
	if strings.Count(block.Content, "\n")+1 != 50 || !strings.Contains(block.Content, `"/48"`) ||
		strings.Contains(block.Content, `"/49"`) {
		t.Fatalf("incorrect bound: %q", block.Content)
	}
	live[0].Name = strings.Repeat("字", 1600)
	block, _ = RenderFileIndex(nil, live)
	if len(block.Content) < 4093 || len(block.Content) > 4096 || !utf8.ValidString(block.Content) ||
		strings.Contains(block.Content, "\n") {
		t.Fatalf("byte bound: %d bytes, valid UTF-8=%v", len(block.Content), utf8.ValidString(block.Content))
	}
	// Multi-line index must drop the overflow line cleanly instead of emitting a truncated path.
	var multi []FileIndexEntry
	for i := range 45 {
		multi = append(multi, FileIndexEntry{
			URI:    fmt.Sprintf("/workspace/reports/section-%02d-%s.md", i, strings.Repeat("x", 70)),
			Source: "workspace", Action: "written", Accessible: true,
		})
	}
	block, ok = RenderFileIndex(nil, multi)
	if !ok || len(block.Content) > 4096 {
		t.Fatalf("multi-line byte bound failed: ok=%v len=%d", ok, len(block.Content))
	}
	for _, line := range strings.Split(block.Content, "\n") {
		if !strings.HasSuffix(line, `.md"`) {
			t.Fatalf("truncated partial line emitted: %q", line)
		}
	}
	if _, ok = RenderFileIndex(nil, nil); ok {
		t.Fatal("rendered empty index")
	}
}

func TestRoundReductionKeepsOneFilesPinAndRecountsMedia(t *testing.T) {
	query := "inspect the attachment"
	files := []FileIndexEntry{{URI: "/output", Source: "workspace", Action: "written", Accessible: true}}
	image := Attachment{Kind: "image", URI: "https://media.test/reference", Name: "reference"}
	in := BuildInput{
		FileIndex: files,
		CurrentTurn: []Message{
			{Role: RoleUser, Content: query, Attachments: []Attachment{image}},
			{Role: RoleAssistant, Content: strings.Repeat("old decision ", 100)},
			{Role: RoleAssistant, Content: "latest"},
		},
		Policy: Policy{Version: PolicyVersionV4}, AllowDropCurrentRounds: true,
	}
	original := mustBuild(t, in)
	in.Budget.MaxInputBytes = 390
	got := mustBuild(t, in)
	if got.DroppedRounds != 2 || len(got.Messages) != 2 ||
		strings.Count(got.Messages[0].Content, `label="files"`) != 1 ||
		!strings.Contains(got.Messages[0].Content, image.URI) ||
		!strings.Contains(got.Messages[0].Content, `label="query"`) ||
		!strings.Contains(
			got.Messages[0].Content,
			query,
		) || original.Usage.InputTokens-got.Usage.InputTokens != 1024 {
		t.Fatalf("round reduction: %+v (was %+v)", got, original.Usage)
	}
	if got.Usage.InputBytes != measureMessages(nil, got.Messages).bytes {
		t.Fatalf("reported bytes=%d", got.Usage.InputBytes)
	}
	in.CurrentTurn = in.CurrentTurn[:1]
	in.Budget.MaxInputBytes = measureMessages(nil, in.CurrentTurn).bytes
	small := mustBuild(t, in)
	if len(small.Messages) != 1 || small.Messages[0].Content != query || len(small.Messages[0].Attachments) != 1 {
		t.Fatalf("first query displaced by files: %+v", small)
	}

	// Files index must be trimmed before causing extra current-turn rounds to be dropped.
	var bigIndex []FileIndexEntry
	for i := range 20 {
		bigIndex = append(bigIndex, FileIndexEntry{
			URI:    fmt.Sprintf("/workspace/out/file-%02d-%s.txt", i, strings.Repeat("a", 80)),
			Source: "workspace", Action: "written", Accessible: true,
		})
	}
	turn := []Message{
		{Role: RoleUser, Content: query, Attachments: []Attachment{image}},
		{Role: RoleAssistant, Content: strings.Repeat("round1 ", 80)},
		{Role: RoleAssistant, Content: strings.Repeat("round2 ", 30)},
		{Role: RoleAssistant, Content: "latest"},
	}
	// Budget fits [pinned(query), round2, latest] without files, so only 2 rounds (user + round1)
	// should drop; bigIndex must be trimmed instead of forcing round2 to be dropped too.
	pinnedQuery := renderContext("", []PinnedBlock{{Label: "query", Content: query}})
	coreBudget := measureMessages(nil, []Message{pinnedQuery, turn[2], turn[3]}).bytes
	gotCore := mustBuild(t, BuildInput{
		FileIndex:              bigIndex,
		CurrentTurn:            turn,
		Policy:                 Policy{Version: PolicyVersionV4},
		Budget:                 Budget{MaxInputBytes: coreBudget + 10},
		AllowDropCurrentRounds: true,
	})
	if gotCore.DroppedRounds != 2 || len(gotCore.Messages) != 3 ||
		gotCore.Messages[1].Content != turn[2].Content ||
		gotCore.Messages[2].Content != "latest" {
		t.Fatalf(
			"files index caused extra round drop: dropped=%d msgs=%+v",
			gotCore.DroppedRounds,
			gotCore.Messages,
		)
	}
}

func TestDecodeSnapshotBeforeAttachments(t *testing.T) {
	payload := json.RawMessage(
		`{"memoryMode":"Off","materials":{"systemInstruction":"system","history":[{"source":{"kind":"History","id":"old"},"messages":[{"role":"user","content":"question"}]}]},"policy":{"version":"context-v4"},"budget":{}}`,
	)
	previous, err := DecodeSnapshot(Envelope{
		Format: SnapshotFormatV1, PolicyVersion: PolicyVersionV4, Digest: digest(payload), Payload: payload,
	})
	if err != nil || previous.Materials.FileIndex != nil ||
		previous.Materials.History[0].Messages[0].Attachments != nil {
		t.Fatalf("snapshot = %+v, err=%v", previous, err)
	}
}
