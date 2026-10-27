package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// TTD 본체 배포처. 구글 드라이브의 다운로드 한도 문제로 GitHub Releases로 옮겼다 — 드라이브는
// GitHub에 접속할 수 없을 때만 쓰는 대비용으로 남아 있다(app.go의 run).
const (
	ttdRepo         = "DiabloTTD/TTD"
	ttdReleasesPage = "https://github.com/" + ttdRepo + "/releases"
)

// latestGitHubZip은 DiabloTTD/TTD의 최신 릴리즈에 첨부된 TTD zip을 찾는다. 버전은 태그(vX.Y.Z)를
// 우선 쓰고, 태그가 버전 형식이 아니면 zip 이름에서 읽는다. /releases/latest라 초안·미리보기는
// GitHub가 알아서 뺀다.
func latestGitHubZip(client *http.Client) (zipCandidate, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+ttdRepo+"/releases/latest", nil)
	if err != nil {
		return zipCandidate{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", browserUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return zipCandidate{}, fmt.Errorf("GitHub에 연결하지 못했습니다: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zipCandidate{}, fmt.Errorf("GitHub 응답 오류 (HTTP %d)", resp.StatusCode)
	}

	var rel struct {
		TagName string               `json:"tag_name"`
		HTMLURL string               `json:"html_url"`
		Assets  []githubReleaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return zipCandidate{}, fmt.Errorf("GitHub 응답을 읽지 못했습니다: %w", err)
	}
	for _, a := range rel.Assets {
		if !strings.HasSuffix(strings.ToLower(a.Name), ".zip") {
			continue
		}
		ver := strings.TrimPrefix(rel.TagName, "v")
		if _, ok := parseVersion(ver); !ok {
			ver = versionFromName(a.Name)
		}
		return zipCandidate{name: a.Name, ver: ver, url: a.BrowserDownloadURL, size: a.Size, page: rel.HTMLURL}, nil
	}
	return zipCandidate{}, fmt.Errorf("최신 릴리즈(%s)에 zip 파일이 없습니다", rel.TagName)
}

// newDownloadClient는 큰 zip(100MB 이상)을 받는 클라이언트다. 전체 시간 제한을 짧게 걸면 느린
// 회선에서 받는 도중 끊기므로, 응답 시작까지만 짧게 기다리고 본문은 넉넉히 허용한다.
func newDownloadClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t, Timeout: 60 * time.Minute}
}

// downloadURLFile은 일반 URL(GitHub 릴리즈 첨부 파일)을 destPath로 받는다. expectedSize와 받은
// 크기가 다르면(중간에 끊김) 실패로 처리한다. progress는 받는 동안 주기적으로 불린다(nil 가능).
func downloadURLFile(rawURL, destPath string, expectedSize int64, client *http.Client, progress func(written, total int64)) error {
	resp, err := doGet(client, rawURL)
	if err != nil {
		return fmt.Errorf("다운로드 서버에 연결하지 못했습니다: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("다운로드 서버가 요청을 거부했습니다 (HTTP %d)", resp.StatusCode)
	}
	total := expectedSize
	if total <= 0 {
		total = resp.ContentLength
	}
	return saveBody(resp.Body, destPath, total, progress)
}

// saveBody는 응답 본문을 destPath에 쓰면서 progress를 알리고, total(알면)과 받은 크기를 맞춰 본다.
// GitHub/드라이브 다운로드가 함께 쓴다.
func saveBody(body io.Reader, destPath string, total int64, progress func(written, total int64)) error {
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("임시 파일을 만들지 못했습니다: %w", err)
	}
	pw := &progressWriter{w: out, total: total, report: progress}
	written, copyErr := io.Copy(pw, body)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("다운로드 중 연결이 끊겼습니다 (%s 받음): %w", formatBytes(written, total), copyErr)
	}
	if total > 0 && written != total {
		return fmt.Errorf("다운로드가 중간에 끊겼습니다 (%s 받음)", formatBytes(written, total))
	}
	if closeErr != nil {
		return fmt.Errorf("받은 파일을 저장하지 못했습니다: %w", closeErr)
	}
	return nil
}

// progressWriter는 10% 단위로(전체 크기를 모르면 10MB 단위로) report를 부른다 — 진행 로그가
// 너무 촘촘해지지 않게.
type progressWriter struct {
	w       io.Writer
	written int64
	total   int64
	next    int64
	report  func(written, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.report != nil {
		step := int64(10 << 20)
		if p.total > 0 {
			step = p.total / 10
		}
		if p.next == 0 {
			p.next = step
		}
		if p.written >= p.next && (p.total <= 0 || p.written < p.total) {
			p.report(p.written, p.total)
			p.next += step
		}
	}
	return n, err
}
