package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
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

// getRemoteZipInfoWithoutDownload는 다운로드 없이 구글 드라이브 폴더 HTML만 스캔하여
// ZIP 파일명과 추출된 버전을 반환한다. 실패 시 ("", "")를 반환한다.
func getRemoteZipInfoWithoutDownload(folderURL string) (string, string) {
	client := newHTTPClient(10 * time.Second)
	resp, err := doGet(client, folderURL)
	if err != nil {
		fmt.Printf("[알림] 온라인 버전 사전 확인 실패 (일반 방식으로 전환): %v\n", err)
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ""
	}
	text := string(body)

	zipNameRe := regexp.MustCompile(`[a-zA-Z0-9_\-.]+\.zip`)
	verRe := regexp.MustCompile(`(?i)TTD[_-]?v?(\d+\.\d+\.\d+)`)
	genRe := regexp.MustCompile(`(\d+\.\d+\.\d+)`)

	type foundEntry struct{ name, ver string }
	seen := map[string]bool{}
	var found []foundEntry
	for _, name := range zipNameRe.FindAllString(text, -1) {
		if seen[name] {
			continue
		}
		seen[name] = true
		if m := verRe.FindStringSubmatch(name); m != nil {
			found = append(found, foundEntry{name, m[1]})
		} else if m := genRe.FindStringSubmatch(name); m != nil {
			found = append(found, foundEntry{name, m[1]})
		}
	}
	if len(found) == 0 {
		return "", ""
	}
	sort.Slice(found, func(i, j int) bool {
		pi, _ := parseVersion(found[i].ver)
		pj, _ := parseVersion(found[j].ver)
		for k := 0; k < 3; k++ {
			if pi[k] != pj[k] {
				return pi[k] > pj[k]
			}
		}
		return false
	})
	return found[0].name, found[0].ver
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

// downloadDriveFile은 단일 구글 드라이브 파일을 확인 토큰(대용량 경고) 처리까지 포함해
// destPath로 내려받는다.
func downloadDriveFile(fileID, destPath string, client *http.Client) error {
	u := "https://drive.google.com/uc?id=" + fileID
	var resp *http.Response

	for i := 0; i < 5; i++ {
		r, err := doGet(client, u)
		if err != nil {
			return err
		}
		contentType := r.Header.Get("Content-Type")
		contentDisp := r.Header.Get("Content-Disposition")
		if strings.Contains(contentType, "text/html") && contentDisp == "" {
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				return err
			}
			confirmURL := getURLFromConfirmationPage(string(body))
			if confirmURL == "" {
				return fmt.Errorf("다운로드 확인 페이지에서 실제 파일 URL을 찾지 못했습니다")
			}
			u = confirmURL
			continue
		}
		resp = r
		break
	}
	if resp == nil {
		return fmt.Errorf("다운로드 확인 페이지 리다이렉트가 반복되어 중단했습니다")
	}
	defer resp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func versionFromName(name string) string {
	verRe := regexp.MustCompile(`(?i)TTD[_-]?v?(\d+\.\d+\.\d+)`)
	if m := verRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return "0.0.0"
}
