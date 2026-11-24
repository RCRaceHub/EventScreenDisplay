// Package image builds the EventScreen SD card image. This test checks the
// Raspberry Pi Imager repository file that goes with it.
package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImagerJSONDescribesTheImage(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("needs jq")
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "eventscreen.img")
	xz := filepath.Join(dir, "eventscreen-0.8.0-rc1-arm64.img.xz")
	if err := os.WriteFile(img, []byte("raw image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xz, []byte("xz"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/RCRaceHub/EventScreenDisplay/releases/download/v0.8.0-rc1/eventscreen-0.8.0-rc1-arm64.img.xz"
	out, err := exec.Command("bash", "imager-json.sh", img, xz, url, "2026-11-24").Output()
	if err != nil {
		t.Fatal(err)
	}
	var repo struct {
		Imager struct {
			Devices []struct {
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			} `json:"devices"`
		} `json:"imager"`
		OSList []struct {
			Name         string   `json:"name"`
			URL          string   `json:"url"`
			ExtractSize  int64    `json:"extract_size"`
			ExtractSHA   string   `json:"extract_sha256"`
			DownloadSize int64    `json:"image_download_size"`
			DownloadSHA  string   `json:"image_download_sha256"`
			ReleaseDate  string   `json:"release_date"`
			InitFormat   string   `json:"init_format"`
			Devices      []string `json:"devices"`
		} `json:"os_list"`
	}
	if err := json.Unmarshal(out, &repo); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(repo.OSList) != 1 {
		t.Fatalf("os_list %+v", repo.OSList)
	}
	entry := repo.OSList[0]
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	if entry.Name != "EventScreen 0.8.0-rc1" || entry.URL != url || entry.ReleaseDate != "2026-11-24" {
		t.Errorf("entry %+v", entry)
	}
	if entry.ExtractSize != 9 || entry.ExtractSHA != sum("raw image") || entry.DownloadSize != 2 || entry.DownloadSHA != sum("xz") {
		t.Errorf("sizes or hashes wrong: %+v", entry)
	}
	// Without this Imager doesn't offer hostname, Wi-Fi and SSH settings.
	if entry.InitFormat != "cloudinit-rpi" {
		t.Errorf("init_format %q", entry.InitFormat)
	}
	zero2 := false
	for _, d := range repo.Imager.Devices {
		if d.Name == "Raspberry Pi Zero 2 W" && len(d.Tags) == 1 && d.Tags[0] == "pi3-64bit" {
			zero2 = true
		}
	}
	if !zero2 {
		t.Errorf("Pi Zero 2 W missing from devices: %+v", repo.Imager.Devices)
	}
}

func TestImagerJSONRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"a.img", "eventscreen-1.0.0-arm64.img.xz", "https://x", "24/11/2026"},
		{"a.img", "wrong-name.img.xz", "https://x", "2026-11-24"},
	} {
		if err := exec.Command("bash", append([]string{"imager-json.sh"}, args...)...).Run(); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}
