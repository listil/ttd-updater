package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// updaterVersion은 이 빌드 자신의 버전이다. GitHub Release 태그(vX.Y.Z)를 새로 발행할 때마다
// 수동으로 맞춰 올려야 한다 — TTD 본체의 version_info.json/버전 체계와는 완전히 별개다.
const updaterVersion = "1.2.0"

const updaterRepo = "listil/ttd-updater"

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

// checkSelfUpdate는 GitHub Releases에서 ttd_updater 자신의 최신 버전을 확인한다. 네트워크
// 문제나 API 오류가 나도 이건 부가 기능일 뿐이므로 에러를 그냥 삼키고 "새 버전 없음"으로
// 취급한다 — TTD 본체 업데이트 흐름을 절대 막지 않는다.
func checkSelfUpdate() (version, downloadURL string, size int64, ok bool) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", updaterRepo),
		nil,
	)
	if err != nil {
		return "", "", 0, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", browserUserAgent)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", "", 0, false
	}
	defer resp.Body.Close()

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", 0, false
	}

	latestVer := strings.TrimPrefix(rel.TagName, "v")
	if !isNewerVersion(latestVer, updaterVersion) {
		return "", "", 0, false
	}

	for _, asset := range rel.Assets {
		if asset.Name == "ttd_updater.exe" {
			return latestVer, asset.BrowserDownloadURL, asset.Size, true
		}
	}
	return "", "", 0, false
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
