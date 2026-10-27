package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// TTD 본체 배포처. 구글 드라이브의 다운로드 한도 문제로 GitHub Releases로 옮겼다 — 드라이브는
// GitHub에서 확인하거나 받을 수 없을 때만 쓰는 대비용으로 남아 있다(app.go의 run/applyUpdate).
const (
	ttdRepo         = "DiabloTTD/TTD"
	ttdReleasesPage = "https://github.com/" + ttdRepo + "/releases"
)

// githubAPIGet은 GitHub REST API(api.github.com + path)를 호출해 JSON을 v로 읽는다. 런처 자체
// 업데이트(checkSelfUpdate)와 TTD 확인(latestGitHubZip)이 같이 쓴다.
func githubAPIGet(client *http.Client, path string, v any) error {
	req, err := http.NewRequest("GET", "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", browserUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub에 연결하지 못했습니다: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API 응답 오류 (HTTP %d)", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GitHub 응답을 읽지 못했습니다: %w", err)
	}
	return nil
}

// latestGitHubZip은 DiabloTTD/TTD의 최신 릴리즈에 첨부된 TTD zip을 찾는다. 먼저 API를 쓰고,
// 실패하면(대개 로그인 없는 API의 IP당 시간당 60회 제한 — PC방처럼 IP를 여럿이 나눠 쓰면 금방
// 걸린다) 횟수 제한이 없는 github.com 웹 페이지로 같은 정보를 읽는다.
func latestGitHubZip(client *http.Client) (zipCandidate, error) {
	z, apiErr := latestGitHubZipAPI(client)
	if apiErr == nil {
		return z, nil
	}
	z, webErr := latestGitHubZipWeb(client)
	if webErr == nil {
		return z, nil
	}
	return zipCandidate{}, fmt.Errorf("%v / 웹: %v", apiErr, webErr)
}

func latestGitHubZipAPI(client *http.Client) (zipCandidate, error) {
	var rel struct {
		TagName string               `json:"tag_name"`
		HTMLURL string               `json:"html_url"`
		Assets  []githubReleaseAsset `json:"assets"`
	}
	if err := githubAPIGet(client, "/repos/"+ttdRepo+"/releases/latest", &rel); err != nil {
		return zipCandidate{}, err
	}
	var assets []zipCandidate
	for _, a := range rel.Assets {
		assets = append(assets, zipCandidate{name: a.Name, url: a.BrowserDownloadURL, size: a.Size})
	}
	return pickReleaseZip(rel.TagName, rel.HTMLURL, assets)
}

var releaseAssetHrefRe = regexp.MustCompile(`href="(/` + regexp.QuoteMeta(ttdRepo) + `/releases/download/[^"]+)"`)

// latestGitHubZipWeb은 API 없이 최신 릴리즈를 읽는다 — /releases/latest가 /releases/tag/<태그>로
// 넘겨주는 주소에서 태그를, 그 태그의 첨부 파일 목록 조각(expanded_assets)에서 zip 링크를 얻는다.
// 크기는 모르므로(0) 다운로드 때 Content-Length로 확인한다.
func latestGitHubZipWeb(client *http.Client) (zipCandidate, error) {
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := doGet(&noRedirect, ttdReleasesPage+"/latest")
	if err != nil {
		return zipCandidate{}, err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/releases/tag/")
	if i < 0 {
		return zipCandidate{}, fmt.Errorf("최신 릴리즈 주소를 알 수 없습니다 (HTTP %d)", resp.StatusCode)
	}
	tag := loc[i+len("/releases/tag/"):]

	resp, err = doGet(client, ttdReleasesPage+"/expanded_assets/"+tag)
	if err != nil {
		return zipCandidate{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zipCandidate{}, fmt.Errorf("릴리즈 첨부 파일 목록 응답 오류 (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return zipCandidate{}, err
	}
	var assets []zipCandidate
	for _, m := range releaseAssetHrefRe.FindAllStringSubmatch(string(body), -1) {
		href := m[1]
		assets = append(assets, zipCandidate{name: href[strings.LastIndex(href, "/")+1:], url: "https://github.com" + href})
	}
	return pickReleaseZip(tag, loc, assets)
}

// pickReleaseZip은 릴리즈 첨부 파일 중 설치할 TTD zip을 고른다. 릴리즈에 다른 zip(소스, 애드온
// 등)이 같이 붙어도 엉뚱한 것을 설치하지 않도록, 이름에 태그와 같은 버전이 든 zip → 이름이
// TTD로 시작하는 zip 순으로 찾고, 그래도 없으면 zip이 딱 하나일 때만 그것을 쓴다.
func pickReleaseZip(tag, page string, assets []zipCandidate) (zipCandidate, error) {
	ver := strings.TrimPrefix(tag, "v")
	if _, ok := parseVersion(ver); !ok {
		ver = versionFromName(tag)
	}
	var zips []zipCandidate
	for _, a := range assets {
		if strings.HasSuffix(strings.ToLower(a.name), ".zip") {
			a.page = page
			zips = append(zips, a)
		}
	}
	pick := func(match func(zipCandidate) bool) (zipCandidate, bool) {
		for _, z := range zips {
			if match(z) {
				return z, true
			}
		}
		return zipCandidate{}, false
	}
	z, ok := pick(func(z zipCandidate) bool { return ver != "0.0.0" && versionFromName(z.name) == ver })
	if !ok {
		z, ok = pick(func(z zipCandidate) bool { return strings.HasPrefix(strings.ToLower(z.name), "ttd") })
	}
	if !ok && len(zips) == 1 {
		z, ok = zips[0], true
	}
	if !ok {
		return zipCandidate{}, fmt.Errorf("최신 릴리즈(%s)에서 TTD zip 파일을 찾지 못했습니다", tag)
	}
	if ver == "0.0.0" {
		ver = versionFromName(z.name)
	}
	z.ver = ver
	return z, nil
}

// newDownloadClient는 큰 zip(100MB 이상)을 받는 클라이언트다. 전체 시간 제한을 짧게 걸면 느린
// 회선에서 받는 도중 끊기므로, 응답 시작까지만 짧게 기다리고 본문은 넉넉히 허용한다. 받는 도중
// 멈춘 연결은 saveBody의 downloadIdleTimeout이 끊는다.
func newDownloadClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t, Timeout: 60 * time.Minute}
}

// downloadURLFile은 일반 URL(GitHub 릴리즈 첨부 파일)을 destPath로 받는다. expectedSize(모르면 0이면
// Content-Length)와 받은 크기가 다르면(중간에 끊김) 실패로 처리한다. progress는 받는 동안
// 주기적으로 불린다(nil 가능).
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

// downloadIdleTimeout 동안 데이터가 한 바이트도 오지 않으면 연결이 멈춘 것으로 보고 끊는다 —
// 끊기지 않고 멈추기만 한 연결이 "업데이트 중..."을 수십 분 붙잡지 않게.
var downloadIdleTimeout = 60 * time.Second

var errDownloadStalled = errors.New("연결이 멈춰 다운로드를 중단했습니다")

// saveBody는 응답 본문을 destPath에 쓰면서 progress를 알리고, total(알면)과 받은 크기를 맞춰 본다.
// GitHub/드라이브 다운로드가 함께 쓴다.
func saveBody(body io.ReadCloser, destPath string, total int64, progress func(written, total int64)) error {
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("임시 파일을 만들지 못했습니다: %w", err)
	}
	// 멈춘 Read는 다른 고루틴에서 본문을 닫아야 풀린다. 데이터가 올 때마다 타이머를 다시 건다.
	var stalled atomic.Bool
	idle := time.AfterFunc(downloadIdleTimeout, func() {
		stalled.Store(true)
		body.Close()
	})
	pw := &progressWriter{w: out, total: total, report: progress, onWrite: func() { idle.Reset(downloadIdleTimeout) }}
	written, copyErr := io.Copy(pw, body)
	idle.Stop()
	closeErr := out.Close()
	if copyErr != nil {
		if stalled.Load() {
			copyErr = fmt.Errorf("%w (%s 동안 데이터 없음)", errDownloadStalled, downloadIdleTimeout)
		}
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
// 너무 촘촘해지지 않게. onWrite는 쓸 때마다 불린다(멈춤 감지 타이머 갱신).
type progressWriter struct {
	w       io.Writer
	written int64
	total   int64
	next    int64
	report  func(written, total int64)
	onWrite func()
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.onWrite != nil {
		p.onWrite()
	}
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
