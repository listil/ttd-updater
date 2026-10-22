package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// TTD 공식 사이트에 올라가 있는 업데이트 노트(설치 폴더의 TTD_ver_note.txt와 같은 내용).
// zip을 받기 전에 "이번에 뭐가 바뀌는지"를 보여주려고 사이트 쪽을 읽는다 — zip 안의 사본은
// 다운로드와 압축 해제가 끝나야 읽을 수 있기 때문.
const releaseNotesURL = "https://ttdiablo.com/TTD_ver_note.txt"

// 노트가 아무리 길어도 로그 창을 뒤덮지 않도록 보여줄 섹션 수를 제한한다(버전 정보가 없는
// 새 설치(0.0.0)이면 전체 이력이 다 "새 버전"이 되므로 특히 필요).
const maxNoteSections = 3

// 각 버전 섹션의 시작 줄: " ▶ TTD v3.4.5 - 2026.10.22"
var noteSectionRe = regexp.MustCompile(`^\s*▶\s*TTD\s*v?(\d+\.\d+\.\d+)`)

// fetchReleaseNotes는 사이트에서 노트를 받아 localVer보다 새로운 버전 섹션들만 줄 단위로
// 돌려준다(최신순, 최대 maxNoteSections개). 부가 기능이라 네트워크 오류, UTF-8이 아닌 응답,
// 새 섹션 없음(노트가 zip보다 늦게 갱신된 경우 등)은 모두 오류 없이 nil로 처리한다.
func fetchReleaseNotes(localVer string) []string {
	resp, err := doGet(newHTTPClient(10*time.Second), releaseNotesURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || !utf8.Valid(data) {
		return nil
	}
	return newerNoteSections(data, localVer, maxNoteSections)
}

// newerNoteSections는 노트 본문에서 localVer보다 새로운 섹션들의 줄을 뽑는다. 섹션은 최신순으로
// 적혀 있다고 가정하지 않고 섹션마다 버전을 비교한다.
func newerNoteSections(data []byte, localVer string, max int) []string {
	var out []string
	sections := 0
	inNewer := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if m := noteSectionRe.FindStringSubmatch(line); m != nil {
			inNewer = isNewerVersion(m[1], localVer) && sections < max
			if inNewer {
				sections++
				out = trimTrailingBlank(out)
				if len(out) > 0 {
					out = append(out, "") // 섹션 사이 빈 줄은 정확히 하나
				}
			}
		}
		// 연속된 빈 줄은 하나로 합친다
		if inNewer && (line != "" || len(out) > 0 && out[len(out)-1] != "") {
			out = append(out, line)
		}
	}
	out = trimTrailingBlank(out)
	if len(out) == 0 {
		return nil
	}
	header := "=== 업데이트 내용 ==="
	if localVer != "0.0.0" {
		header = fmt.Sprintf("=== 업데이트 내용 (현재 v%s 이후) ===", localVer)
	}
	return append([]string{header}, out...)
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
