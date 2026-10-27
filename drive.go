package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

func doGet(client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	return client.Do(req)
}

func extractFolderID(folderURL string) string {
	u, err := url.Parse(folderURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
	return parts[len(parts)-1]
}

type driveEntry struct {
	id   string
	name string
}

var (
	aTagRe     = regexp.MustCompile(`(?s)<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	fileHrefRe = regexp.MustCompile(`^https://drive\.google\.com/file/d/([-\w]{25,})/view`)
	tagStripRe = regexp.MustCompile(`<[^>]+>`)
)

// listDriveFolder는 'embeddedfolderview' 경량 페이지를 스캔해 폴더 내 파일 목록을 반환한다.
// gdown과 달리 bs4 없이 정규식만으로 파싱해 의존성을 줄인다(용량/Defender 오탐 완화 목적).
func listDriveFolder(folderID string, client *http.Client) ([]driveEntry, error) {
	u := "https://drive.google.com/embeddedfolderview?" + url.Values{"id": {folderID}}.Encode()
	resp, err := doGet(client, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("폴더 목록 조회 실패 (status %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)

	var entries []driveEntry
	for _, m := range aTagRe.FindAllStringSubmatch(text, -1) {
		href, rawName := m[1], m[2]
		fm := fileHrefRe.FindStringSubmatch(href)
		if fm == nil {
			continue
		}
		name := strings.TrimSpace(html.UnescapeString(tagStripRe.ReplaceAllString(rawName, "")))
		entries = append(entries, driveEntry{id: fm[1], name: name})
	}
	return entries, nil
}

// getURLFromConfirmationPage는 대용량 파일 다운로드 시 뜨는 '바이러스 검사 경고' 페이지에서
// 실제 다운로드 URL을 추출한다. 못 찾으면 빈 문자열을 반환한다.
func getURLFromConfirmationPage(htmlText string) string {
	if m := regexp.MustCompile(`href="(/uc\?export=download[^"]+)"`).FindStringSubmatch(htmlText); m != nil {
		return strings.ReplaceAll("https://docs.google.com"+m[1], "&amp;", "&")
	}

	formTagRe := regexp.MustCompile(`(?s)<form[^>]*id="download-form"[^>]*>`)
	if loc := formTagRe.FindStringIndex(htmlText); loc != nil {
		formTag := htmlText[loc[0]:loc[1]]
		if am := regexp.MustCompile(`action="([^"]+)"`).FindStringSubmatch(formTag); am != nil {
			action := strings.ReplaceAll(am[1], "&amp;", "&")
			rest := htmlText[loc[1]:]
			formBody := rest
			if endIdx := strings.Index(rest, "</form>"); endIdx != -1 {
				formBody = rest[:endIdx]
			}
			if u, err := url.Parse(action); err == nil {
				q := u.Query()
				inputRe := regexp.MustCompile(`<input[^>]*type="hidden"[^>]*>`)
				nameRe := regexp.MustCompile(`name="([^"]+)"`)
				valueRe := regexp.MustCompile(`value="([^"]*)"`)
				for _, inp := range inputRe.FindAllString(formBody, -1) {
					nm := nameRe.FindStringSubmatch(inp)
					if nm == nil {
						continue
					}
					val := ""
					if vm := valueRe.FindStringSubmatch(inp); vm != nil {
						val = vm[1]
					}
					q.Set(nm[1], val)
				}
				u.RawQuery = q.Encode()
				return u.String()
			}
		}
	}

	if m := regexp.MustCompile(`"downloadUrl":"([^"]+)"`).FindStringSubmatch(htmlText); m != nil {
		s := strings.ReplaceAll(m[1], `=`, "=")
		s = strings.ReplaceAll(s, `&`, "&")
		return s
	}

	return ""
}

// errDriveQuota는 구글 드라이브가 파일별 다운로드 한도 초과로 다운로드를 막았다는 뜻이다.
// 새 TTD 버전이 올라와 사용자가 한꺼번에 몰리면 생기며, 최대 24시간 그 파일은 어떤 방법으로도
// 받을 수 없을 수 있다 — 업데이터 쪽 버그가 아니므로 호출하는 쪽이 따로 안내한다.
var errDriveQuota = errors.New("구글 드라이브 다운로드 한도 초과")

// 한도 초과 안내 페이지의 문구. 드라이브 화면 언어에 따라 영어/한국어로 온다.
var driveQuotaMarkers = []string{
	"Too many users have viewed or downloaded this file recently",
	"download quota",
	"최근에 이 파일을 보거나 다운로드한 사용자가 너무 많습니다",
	"다운로드 할당량",
}

func isDriveQuotaPage(body string) bool {
	for _, m := range driveQuotaMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// downloadDriveFile은 단일 구글 드라이브 파일을 확인 토큰(대용량 경고) 처리까지 포함해
// destPath로 내려받는다. 실패하면 "어느 단계에서 왜" 실패했는지(드라이브가 보낸 페이지 내용,
// HTTP 상태, 받다가 끊긴 바이트 수)를 오류 문구에 그대로 담는다 — 사용자 화면의 진행 로그만
// 보고도 원인(주소를 못 찾음 / 드라이브가 거부 / 받다가 끊김)을 구분할 수 있게.
func downloadDriveFile(fileID, destPath string, client *http.Client, progress func(written, total int64)) error {
	u := "https://drive.google.com/uc?id=" + fileID
	var resp *http.Response

	for i := 0; i < 5; i++ {
		r, err := doGet(client, u)
		if err != nil {
			return fmt.Errorf("드라이브에 연결하지 못했습니다: %w", err)
		}
		contentType := r.Header.Get("Content-Type")
		contentDisp := r.Header.Get("Content-Disposition")
		if strings.Contains(contentType, "text/html") && contentDisp == "" {
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				return fmt.Errorf("드라이브 응답을 읽다가 연결이 끊겼습니다: %w", err)
			}
			if isDriveQuotaPage(string(body)) {
				return errDriveQuota
			}
			confirmURL := getURLFromConfirmationPage(string(body))
			if confirmURL == "" {
				return fmt.Errorf("드라이브가 파일 대신 안내 페이지를 보냈습니다 (HTTP %d, %s)",
					r.StatusCode, drivePageReason(string(body)))
			}
			u = confirmURL
			continue
		}
		if r.StatusCode != http.StatusOK {
			r.Body.Close()
			return fmt.Errorf("드라이브가 다운로드를 거부했습니다 (HTTP %d %s)", r.StatusCode, contentType)
		}
		resp = r
		break
	}
	if resp == nil {
		return fmt.Errorf("다운로드 확인 페이지 리다이렉트가 반복되어 중단했습니다")
	}
	defer resp.Body.Close()

	return saveBody(resp.Body, destPath, resp.ContentLength, progress)
}

// formatBytes는 "12.3MB / 45.6MB" 형식으로 받은 양을 보여준다(전체 크기를 모르면 받은 양만).
func formatBytes(written, total int64) string {
	mb := func(n int64) string { return fmt.Sprintf("%.1fMB", float64(n)/(1<<20)) }
	if total > 0 {
		return mb(written) + " / " + mb(total)
	}
	return mb(written)
}

var (
	driveCaptionRe = regexp.MustCompile(`(?s)class="uc-error-(?:sub)?caption"[^>]*>(.*?)</(?:p|div)>`)
	driveTitleRe   = regexp.MustCompile(`(?s)<title>(.*?)</title>`)
	tagRe          = regexp.MustCompile(`<[^>]+>`)
)

// drivePageReason은 드라이브가 보낸 안내 페이지에서 사람이 읽을 이유를 뽑는다 — 오류 문구
// (uc-error-caption/subcaption)가 있으면 그것을, 없으면 페이지 제목을 쓴다.
func drivePageReason(body string) string {
	var parts []string
	for _, m := range driveCaptionRe.FindAllStringSubmatch(body, -1) {
		if t := cleanPageText(m[1]); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		if m := driveTitleRe.FindStringSubmatch(body); m != nil {
			parts = append(parts, "페이지 제목: "+cleanPageText(m[1]))
		}
	}
	if len(parts) == 0 {
		return "알 수 없는 페이지"
	}
	reason := strings.Join(parts, " ")
	if r := []rune(reason); len(r) > 200 {
		reason = string(r[:200]) + "…"
	}
	return reason
}

func cleanPageText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(s, " "))), " ")
}

func versionFromName(name string) string {
	verRe := regexp.MustCompile(`(?i)TTD[_-]?v?(\d+\.\d+\.\d+)`)
	if m := verRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return "0.0.0"
}
