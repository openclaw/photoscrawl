package archive

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadIDs(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		want    []string
		invalid bool
	}{
		{name: "lines", input: " asset:a\r\n\nasset:b\n", want: []string{"asset:a", "asset:b"}},
		{name: "bracketed filename", input: "[cover].jpg\nsecond.jpg", want: []string{"[cover].jpg", "second.jpg"}},
		{name: "braced filename", input: "{cover}.jpg\nsecond.jpg", want: []string{"{cover}.jpg", "second.jpg"}},
		{name: "bracketed keyword filename", input: "[nature].jpg", want: []string{"[nature].jpg"}},
		{name: "empty brackets filename", input: "[]cover.jpg", want: []string{"[]cover.jpg"}},
		{name: "spaced brackets filename", input: "[ ].jpg", want: []string{"[ ].jpg"}},
		{name: "empty braces filename", input: "{}.jpg", want: []string{"{}.jpg"}},
		{name: "array", input: " \n [\"asset:a\", \"asset:b\"]\n", want: []string{"asset:a", "asset:b"}},
		{name: "empty array", input: "[]", want: []string{}},
		{name: "empty lines", input: "\n \n"},
		{name: "truncated array", input: "[\"asset:a\"", invalid: true},
		{name: "trailing comma", input: "[\"asset:a\",]", invalid: true},
		{name: "mixed types", input: "[\"asset:a\", 7, \"asset:b\"]", invalid: true},
		{name: "object", input: "{\"ids\":[\"asset:a\"]}", invalid: true},
		{name: "numeric array", input: "[7]", invalid: true},
		{name: "null entry", input: "[\"asset:a\", null]", invalid: true},
		{name: "blank entry", input: "[\"asset:a\", \" \"]", invalid: true},
		{name: "trailing data", input: "[\"asset:a\"]\nasset:b", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ids.txt")
			if err := os.WriteFile(path, []byte(test.input), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readIDs(path)
			if test.invalid {
				if err == nil || len(got) != 0 {
					t.Fatalf("readIDs = %q, %v; want error without partial IDs", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("readIDs = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestFindRejectsMalformedExclusions(t *testing.T) {
	paths := curationFixture(t)
	path := filepath.Join(t.TempDir(), "exclude.json")
	if err := os.WriteFile(path, []byte("[\"find-both\",]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := Find(context.Background(), paths, FindOptions{ExcludeIDsFile: path}); err == nil {
		t.Fatalf("Find ignored malformed exclusions and returned %d assets", len(result.Assets))
	}
}
