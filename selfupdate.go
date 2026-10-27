package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// updaterVersion은 이 빌드 자신의 버전이다. GitHub Release 태그(vX.Y.Z)를 새로 발행할 때마다
// 수동으로 맞춰 올려야 한다 — TTD 본체의 version_info.json/버전 체계와는 완전히 별개다.
const updaterVersion = "1.4.1"

const updaterRepo = "listil/ttd-updater"

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	Body        string               `json:"body"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

// 런처 노트 카드로 보여줄 최대 릴리즈 수(최신순). 한참 뒤처진 사용자라도 패널이 끝없이
// 길어지지 않게 한다.
const maxLauncherNotes = 10

// selfUpdateInfo는 설치할 수 있는 ttd_updater 새 버전 정보다. Notes는 업데이트 내역 패널 맨 위
// "런처" 카드로 보여줄 릴리즈 노트들(현재 버전보다 새로운 것, 최신순, TTD 노트와 같은
// NoteSection 형식)이다.
type selfUpdateInfo struct {
	Version     string
	DownloadURL string
	Size        int64
	Notes       []NoteSection
}

// checkSelfUpdate는 GitHub Releases에서 ttd_updater 자신의 새 버전을 확인한다. 네트워크
// 문제나 API 오류가 나도 이건 부가 기능일 뿐이므로 에러를 그냥 삼키고 "새 버전 없음"으로
// 취급한다 — TTD 본체 업데이트 흐름을 절대 막지 않는다.
//
// /releases/latest 대신 릴리즈 목록을 한 번에 받는다 — 두 버전 이상 뒤처진 사용자에게도
// 사이 버전들의 노트까지 보여주기 위해서다. 목록에는 /releases/latest가 알아서 빼 주던
// 초안(draft)·미리보기(prerelease)도 섞여 오므로 여기서 직접 걸러낸다. 설치 대상은
// ttd_updater.exe가 첨부된 것 중 가장 높은 버전이다.
func checkSelfUpdate() (selfUpdateInfo, bool) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30", updaterRepo),
		nil,
	)
	if err != nil {
		return selfUpdateInfo{}, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", browserUserAgent)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return selfUpdateInfo{}, false
	}
	defer resp.Body.Close()

	var rels []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return selfUpdateInfo{}, false
	}

	var info selfUpdateInfo
	var newer []githubRelease
	for _, rel := range rels {
		ver := strings.TrimPrefix(rel.TagName, "v")
		if rel.Draft || rel.Prerelease || !isNewerVersion(ver, updaterVersion) {
			continue
		}
		newer = append(newer, rel)
		if info.Version != "" && !isNewerVersion(ver, info.Version) {
			continue
		}
		for _, asset := range rel.Assets {
			if asset.Name == "ttd_updater.exe" {
				info.Version = ver
				info.DownloadURL = asset.BrowserDownloadURL
				info.Size = asset.Size
			}
		}
	}
	if info.Version == "" {
		return selfUpdateInfo{}, false
	}

	// 노트는 최신순으로, 설치할 버전까지만(그보다 새 릴리즈는 exe가 없어 설치되지 않음).
	sort.Slice(newer, func(i, j int) bool {
		return isNewerVersion(strings.TrimPrefix(newer[i].TagName, "v"), strings.TrimPrefix(newer[j].TagName, "v"))
	})
	for _, rel := range newer {
		ver := strings.TrimPrefix(rel.TagName, "v")
		if isNewerVersion(ver, info.Version) {
			continue
		}
		if len(info.Notes) == maxLauncherNotes {
			break
		}
		info.Notes = append(info.Notes, releaseNoteSection(ver, rel.PublishedAt, rel.Body))
	}
	return info, true
}

// releaseNoteSection은 GitHub 릴리즈 본문(Markdown)을 업데이트 내역 카드 형식으로 바꾼다.
// 프론트엔드의 renderNoteLine이 "*"를 소제목, "-"를 항목으로 해석하므로 Markdown 제목(#)은
// "*"로, 목록 머리표(* / +)는 "-"로 맞추고, 굵게/코드 표시 기호는 걷어낸다.
func releaseNoteSection(version, publishedAt, body string) NoteSection {
	strip := strings.NewReplacer("**", "", "`", "")
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strip.Replace(line))
		switch {
		case strings.HasPrefix(line, "#"):
			line = "* " + strings.TrimSpace(strings.TrimLeft(line, "#"))
		case strings.HasPrefix(line, "* "), strings.HasPrefix(line, "+ "):
			line = "- " + line[2:]
		}
		lines = append(lines, line)
	}
	// 앞뒤 빈 줄은 카드에 쓸데없는 여백만 만든다.
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	date := publishedAt
	if len(date) >= 10 {
		date = strings.ReplaceAll(date[:10], "-", ".") // 2026-10-27T… → 2026.10.27 (TTD 노트와 같은 표기)
	}
	return NoteSection{Version: version, Date: date, Lines: lines}
}

// downloadSelfUpdate는 새 ttd_updater.exe를 자기 자신 옆에 "ttd_updater.exe.new"로 받아둔다.
// 실행 중인 파일 자체는 Windows에서 직접 덮어쓸 수 없어서, 실제 교체는 종료 직후
// installSelfUpdateAndExit가 띄우는 별도 프로세스가 한다. expectedSize와 실제로 받은
// 바이트 수가 다르면(다운로드 중단/손상) 새 파일을 지우고 실패로 처리해, 손상된 exe로
// 교체되는 일이 없도록 한다.
func downloadSelfUpdate(downloadURL string, expectedSize int64) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	newPath := exePath + ".new"

	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("다운로드 실패 (status %d)", resp.StatusCode)
	}

	out, err := os.Create(newPath)
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(out, resp.Body)
	out.Close()
	if copyErr != nil {
		os.Remove(newPath)
		return "", copyErr
	}
	if expectedSize > 0 && written != expectedSize {
		os.Remove(newPath)
		return "", fmt.Errorf("다운로드한 파일 크기가 일치하지 않습니다 (%d/%d bytes)", written, expectedSize)
	}
	return newPath, nil
}

// installSelfUpdateAndExit는 현재 실행 파일이 종료되어 파일 잠금이 풀리길 기다렸다가
// newPath를 그 자리로 옮기는 헬퍼 프로세스를 띄우고 바로 반환한다(헬퍼를 기다리지 않음 —
// 이 프로세스가 먼저 끝나야 헬퍼의 이동이 성공하므로 당연히 기다리면 안 된다). 헬퍼는 최대
// 30초 동안 0.5초 간격으로 재시도하며, 그래도 실패하면(예: 다른 프로세스가 계속 잠그고 있음)
// 기존 exe를 건드리지 않고 조용히 포기한다 — 실패해도 기존 exe는 항상 안전하게 남는다.
func installSelfUpdateAndExit(newPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	script := fmt.Sprintf(
		`for ($i = 0; $i -lt 60; $i++) { `+
			`try { Move-Item -Force %s %s -ErrorAction Stop; break } `+
			`catch { Start-Sleep -Milliseconds 500 } }`,
		psQuote(newPath), psQuote(exePath),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	return cmd.Start()
}
