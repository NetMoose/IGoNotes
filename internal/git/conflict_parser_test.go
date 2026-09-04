package git

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSplitNULTerminated(t *testing.T) {
	parts, err := splitNULTerminated([]byte("a\x00name with\nnewline\x00"))
	if err != nil || len(parts) != 2 || string(parts[1]) != "name with\nnewline" {
		t.Fatalf("split = %#v, %v", parts, err)
	}
	_, err = splitNULTerminated([]byte("truncated"))
	if !errors.Is(err, ErrOutputMalformed) {
		t.Fatalf("truncated error = %v, want ErrOutputMalformed", err)
	}
	parts, err = splitNULTerminated(nil)
	if err != nil || parts != nil {
		t.Fatalf("empty split = %#v, %v", parts, err)
	}
}

func TestParsePorcelainV2Z(t *testing.T) {
	oid := strings.Repeat("a", 40)
	data := []byte("1 M. N... 100644 100644 100644 " + oid + " " + oid + " -notes/with space\tand\nnewline.md\x00" +
		"2 R. N... 100644 100644 100644 " + oid + " " + oid + " R100 renamed.md\x00old name.md\x00" +
		"2 R. N... 100644 100644 100644 " + oid + " " + oid + " R050 partially-renamed.md\x00old partially-renamed.md\x00" +
		"u UU N... 100644 100644 100644 100644 " + oid + " " + oid + " " + oid + " :(glob)*.md\x00")

	got, err := parsePorcelainV2Z(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []porcelainEntry{
		{RecordType: '1', XY: "M.", Path: "-notes/with space\tand\nnewline.md"},
		{RecordType: '2', XY: "R.", Path: "renamed.md", OriginalPath: "old name.md"},
		{RecordType: '2', XY: "R.", Path: "partially-renamed.md", OriginalPath: "old partially-renamed.md"},
		{RecordType: 'u', XY: "UU", Path: ":(glob)*.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
}

func TestParsePorcelainV2ZRejectsMalformedRecords(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, data := range [][]byte{
		[]byte("? untracked\x00"),
		[]byte("1 M. N... 100644\x00"),
		[]byte("2 R. N... 100644 100644 100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa R100 new.md\x00"),
		[]byte("2 R. N... 100644 100644 100644 " + oid + " " + oid + " R+50 new.md\x00old.md\x00"),
		[]byte("2 R. N... 100644 100644 100644 " + oid + " " + oid + " R0a0 new.md\x00old.md\x00"),
		[]byte("1 M. N... 100644 100644 100644 " + oid + " " + oid + " \x00"),
		[]byte("2 R. N... 100644 100644 100644 " + oid + " " + oid + " R100 new.md\x00\x00"),
		[]byte("u UU N... 100644 100644 100644 100644 " + oid + " " + oid + " " + oid + " \x00"),
		[]byte("1 M. N... 100644 100644 100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa path"),
	} {
		_, err := parsePorcelainV2Z(data)
		if !errors.Is(err, ErrOutputMalformed) {
			t.Fatalf("parsePorcelainV2Z(%q) error = %v, want ErrOutputMalformed", data, err)
		}
	}
}

func TestParseIndexStagesZ(t *testing.T) {
	oid := strings.Repeat("a", 40)
	data := []byte("100644 " + oid + " 0\tfile with space\x00" +
		"100755 " + strings.Repeat("b", 64) + " 1\t-leading-dash\x00" +
		"100644 " + oid + " 2\tpath\twith\ttabs\x00" +
		"100644 " + oid + " 3\tline\nbreak\x00")
	got, err := parseIndexStagesZ(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []indexStage{
		{Path: "file with space", Mode: "100644", OID: oid, Stage: 0},
		{Path: "-leading-dash", Mode: "100755", OID: strings.Repeat("b", 64), Stage: 1},
		{Path: "path\twith\ttabs", Mode: "100644", OID: oid, Stage: 2},
		{Path: "line\nbreak", Mode: "100644", OID: oid, Stage: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
}

func TestParseIndexStagesZRejectsMalformedRecords(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, data := range [][]byte{
		[]byte("100644 " + oid + " 0 path\x00"),
		[]byte("10064x " + oid + " 0\tpath\x00"),
		[]byte("100644 " + oid + " 4\tpath\x00"),
		[]byte("100644 not-an-oid 0\tpath\x00"),
		[]byte("100644 " + oid + " 0\t\x00"),
		[]byte("100644 " + oid + " 0\tpath\x00100644 " + oid + " 0\tpath\x00"),
	} {
		_, err := parseIndexStagesZ(data)
		if !errors.Is(err, ErrOutputMalformed) {
			t.Fatalf("parseIndexStagesZ(%q) error = %v, want ErrOutputMalformed", data, err)
		}
	}
}

func TestParseNameStatusZ(t *testing.T) {
	data := []byte("M\x00-file with space\x00D\x00:(glob)*.md\x00R100\x00old\tname\x00new\nname\x00C100\x00old copy\x00-leading-dash\x00")
	got, err := parseNameStatusZ(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []nameStatus{
		{Status: 'M', Path: "-file with space"},
		{Status: 'D', Path: ":(glob)*.md"},
		{Status: 'R', Score: 100, OldPath: "old\tname", Path: "new\nname"},
		{Status: 'C', Score: 100, OldPath: "old copy", Path: "-leading-dash"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
}

func TestParseNameStatusZRejectsMalformedRecords(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("R100\x00old\x00"),
		[]byte("C100\x00old\x00"),
		[]byte("R99\x00old\x00new\x00"),
		[]byte("R101\x00old\x00new\x00"),
		[]byte("R+50\x00old\x00new\x00"),
		[]byte("C0a0\x00old\x00new\x00"),
		[]byte("M\x00\x00"),
		[]byte("R100\x00\x00new\x00"),
		[]byte("C100\x00old\x00\x00"),
		[]byte("X\x00path\x00"),
	} {
		_, err := parseNameStatusZ(data)
		if !errors.Is(err, ErrOutputMalformed) {
			t.Fatalf("parseNameStatusZ(%q) error = %v, want ErrOutputMalformed", data, err)
		}
	}
}

func TestParseAttributesZ(t *testing.T) {
	data := []byte("-file with space\x00merge\x00ours\x00:(glob)*.md\x00filter\tname\x00value\nwith newline\x00")
	got, err := parseCheckAttrZ(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []attributeRecord{
		{Path: "-file with space", Name: "merge", Value: "ours"},
		{Path: ":(glob)*.md", Name: "filter\tname", Value: "value\nwith newline"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
	for _, data := range [][]byte{[]byte("path\x00name\x00"), []byte("\x00name\x00value\x00")} {
		_, err = parseCheckAttrZ(data)
		if !errors.Is(err, ErrOutputMalformed) {
			t.Fatalf("parseCheckAttrZ(%q) error = %v, want ErrOutputMalformed", data, err)
		}
	}
}

func TestParseCheckoutTempZ(t *testing.T) {
	got, err := parseCheckoutTempZ([]byte(".merge_file_123\t-file with\t tabs\x00nested/temp\t:(glob)*.md\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []checkoutTemp{{TempPath: ".merge_file_123", Path: "-file with\t tabs"}, {TempPath: "nested/temp", Path: ":(glob)*.md"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
	for _, data := range [][]byte{[]byte("temp path\x00"), []byte("../temp\tpath\x00"), []byte("temp\t\x00")} {
		_, err := parseCheckoutTempZ(data)
		if !errors.Is(err, ErrOutputMalformed) {
			t.Fatalf("parseCheckoutTempZ(%q) error = %v, want ErrOutputMalformed", data, err)
		}
	}
}

func FuzzConflictParsers(f *testing.F) {
	oid := strings.Repeat("a", 40)
	for _, seed := range [][]byte{
		[]byte("1 M. N... 100644 100644 100644 " + oid + " " + oid + " path\x00"), []byte("1 M."),
		[]byte("100644 " + oid + " 0\tpath\x00"), []byte("100644 " + oid),
		[]byte("R100\x00old\x00new\x00"), []byte("R100\x00old\x00"),
		[]byte("path\x00name\x00value\x00"), []byte("path\x00name\x00"),
		[]byte("temp\tpath\x00"), []byte("temp\tpath"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parsePorcelainV2Z(data)
		_, _ = parseIndexStagesZ(data)
		_, _ = parseNameStatusZ(data)
		_, _ = parseCheckAttrZ(data)
		_, _ = parseCheckoutTempZ(data)
	})
}
