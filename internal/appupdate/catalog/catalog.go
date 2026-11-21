// Package catalog selects verifiable release assets for native application updates.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/mod/semver"
)

// Repository is the renamed Super Cage repository. GitHub serves asset URLs
// under the current name, so download URLs under the old name are not trusted.
const Repository = "sweetcornna/Super-Cage"
const ReleasesURL = "https://api.github.com/repos/" + Repository + "/releases?per_page=100"

// Releases must stay below the limit of every published App that still updates
// in place; scripts/prepare-release-assets.mjs enforces that smaller limit.
const MaxArchive = 2 << 30

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}
type Candidate struct {
	Version   string `json:"version"`
	Archive   Asset  `json:"archive"`
	Checksums Asset  `json:"checksums"`
}

// archiveNames lists the accepted package names, current name first. Releases
// before the Super Cage rename used the "Cage-" prefix.
func archiveNames(version, goos, goarch string) ([]string, error) {
	var suffix string
	switch {
	case goos == "darwin" && goarch == "arm64":
		suffix = "-macos-arm64.dmg"
	case goos == "windows" && goarch == "amd64":
		suffix = "-windows-amd64-setup.exe"
	// Linux only learns that a newer package exists; the system installs it.
	case goos == "linux" && goarch == "amd64":
		suffix = "-linux-amd64.deb"
	case goos == "android" && goarch == "arm64":
		suffix = "-android-arm64.apk"
	default:
		return nil, errors.New("此平台尚无 Super Cage 应用内更新发行包")
	}
	return []string{"Super-Cage-" + version + suffix, "Cage-" + version + suffix}, nil
}

func Select(releases []Release, current, goos, goarch string) (*Candidate, error) {
	if _, err := archiveNames(current, goos, goarch); err != nil {
		return nil, err
	}
	currentTag := "v" + current
	if !semver.IsValid(currentTag) || semver.Canonical(currentTag) != currentTag {
		return nil, errors.New("当前应用版本无效，无法安全比较更新")
	}
	var best *Candidate
	for _, r := range releases {
		if r.Draft || !strings.HasPrefix(r.Tag, "v") || !semver.IsValid(r.Tag) || semver.Canonical(r.Tag) != r.Tag || semver.Compare(r.Tag, currentTag) <= 0 {
			continue
		}
		pre := r.Prerelease || semver.Prerelease(r.Tag) != ""
		if semver.Prerelease(currentTag) == "" && pre {
			continue
		}
		v := strings.TrimPrefix(r.Tag, "v")
		if best != nil && semver.Compare(r.Tag, "v"+best.Version) <= 0 {
			continue
		}
		c := Candidate{Version: v}
		names, _ := archiveNames(v, goos, goarch)
		rank := len(names)
		for _, a := range r.Assets {
			if a.State != "uploaded" {
				continue
			}
			for i, name := range names {
				if i < rank && a.Name == name && a.Size > 0 && a.Size <= MaxArchive {
					c.Archive, rank = a, i
				}
			}
			if a.Name == "SHA256SUMS" && a.Size > 0 && a.Size <= 64<<10 {
				c.Checksums = a
			}
		}
		if c.Archive.URL == "" {
			continue
		}
		base := "https://github.com/" + Repository + "/releases/download/" + r.Tag + "/"
		if c.Archive.URL != base+c.Archive.Name || (c.Checksums.URL != "" && c.Checksums.URL != base+"SHA256SUMS") {
			continue
		}
		if c.Archive.Digest == "" && c.Checksums.URL == "" {
			continue
		}
		best = &c
	}
	return best, nil
}

func DigestValue(raw string) (string, error) {
	if !strings.HasPrefix(raw, "sha256:") {
		return "", errors.New("发行包缺少有效的 SHA-256 摘要")
	}
	h := strings.TrimPrefix(raw, "sha256:")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != sha256.Size {
		return "", errors.New("发行包 SHA-256 摘要无效")
	}
	return strings.ToLower(h), nil
}

func (c Candidate) SHA256(b []byte) (string, error) {
	var hash string
	if c.Archive.Digest != "" {
		var err error
		hash, err = DigestValue(c.Archive.Digest)
		if err != nil {
			return "", err
		}
	}
	if c.Checksums.URL == "" {
		if hash == "" {
			return "", errors.New("发行包缺少校验信息")
		}
		return hash, nil
	}
	found := ""
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != c.Archive.Name {
			continue
		}
		h, err := DigestValue("sha256:" + fields[0])
		if err != nil || found != "" {
			return "", errors.New("发行包校验清单无效或重复")
		}
		found = h
	}
	if found == "" || (hash != "" && hash != found) {
		return "", errors.New("发行包与校验清单的 SHA-256 不一致")
	}
	return found, nil
}
