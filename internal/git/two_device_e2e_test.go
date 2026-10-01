package git

import "testing"

func TestTwoDeviceEndToEndConvergesCreateEditRenameDeleteAndAssets(t *testing.T) {
	f := newTwoDeviceFixture(t)
	one, two := f.one, f.two
	initial := map[string][]byte{
		"shared.md":       []byte("# Shared\nПривет\n"),
		"delete-me.md":    []byte("delete this note\n"),
		"folder/local.md": []byte("local before\n"),
	}
	for name, contents := range initial {
		one.write(t, name, contents)
	}
	f.initialize(t, one, true)
	f.initialize(t, two, false)
	for name, contents := range initial {
		two.assertContents(t, name, contents)
	}

	created := "created note [one].md"
	renamed := "renamed [two].md"
	asset := "assets/images/binary asset.bin"
	noteBytes := []byte("# Created on one\nexact bytes\r\n")
	assetBytes := []byte{0, 1, 2, 3, 255}
	jsonBytes := []byte("{\"enabled\":true}\n")
	one.write(t, created, noteBytes)
	one.write(t, asset, assetBytes)
	one.write(t, "data.json", jsonBytes)
	f.sync(t, one)
	f.sync(t, two)
	two.assertContents(t, created, noteBytes)
	two.assertContents(t, asset, assetBytes)
	two.assertContents(t, "data.json", jsonBytes)

	two.rename(t, created, renamed)
	two.remove(t, "delete-me.md")
	remoteBytes := []byte("remote addition from two\n")
	localBytes := []byte("local edit from one\n")
	two.write(t, "folder/remote.md", remoteBytes)
	one.write(t, "folder/local.md", localBytes)
	f.sync(t, one)
	f.sync(t, two)
	f.sync(t, one)

	want := map[string][]byte{
		"shared.md": initial["shared.md"], "folder/local.md": localBytes,
		"folder/remote.md": remoteBytes, renamed: noteBytes,
		asset: assetBytes, "data.json": jsonBytes,
	}
	f.assertConverged(t, want, created, "delete-me.md")
}
