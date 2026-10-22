package main

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// TTD 공식 사이트에 올라가 있는 업데이트 노트(설치 폴더의 TTD_ver_note.txt와 같은 내용).
// zip 안의 사본은 다운로드·압축 해제가 끝나야 읽을 수 있으므로, 창이 뜨자마자 업데이트
// 내역 패널을 채우려고 사이트 쪽을 읽는다.
const releaseNotesURL = "https://ttdiablo.com/TTD_ver_note.txt"

// 패널에 보여줄 최대 섹션 수(최신순). 전체 이력은 수십 개라 런처 패널엔 최근 것만 충분하다.
const maxNoteSections = 6

// 각 버전 섹션의 시작 줄: " ▶ TTD v3.4.5 - 2026.10.22"
var noteSectionRe = regexp.MustCompile(`^\s*▶\s*TTD\s*v?(\d+\.\d+\.\d+)\s*(?:-\s*(.*))?$`)

// NoteSection은 업데이트 노트의 버전 하나 분량이다. Lines는 원문 줄 그대로(앞 공백 1칸만
// 정리) 보내고, "*"(소제목)/"-"(항목) 같은 표시는 프론트엔드가 해석해 꾸민다.
type NoteSection struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Lines   []string `json:"lines"`
}

// fetchReleaseNotes는 사이트에서 노트를 받아 최신 섹션들을 돌려준다. 부가 기능이라 네트워크
// 오류나 UTF-8이 아닌 응답 등은 모두 오류 없이 nil로 처리한다(패널은 "불러오지 못함"만 표시).
func fetchReleaseNotes() []NoteSection {
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
	return parseNoteSections(data, maxNoteSections)
}

func parseNoteSections(data []byte, max int) []NoteSection {
	var out []NoteSection
	var cur *NoteSection
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if m := noteSectionRe.FindStringSubmatch(line); m != nil {
			if len(out) == max {
				break
			}
			out = append(out, NoteSection{Version: m[1], Date: strings.TrimSpace(m[2])})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue // 첫 섹션 이전의 "☆ 수정날짜" 같은 머리말은 건너뜀
		}
		line = strings.TrimPrefix(line, " ")
		// 연속된 빈 줄은 하나로, 섹션 맨 앞의 빈 줄은 버림
		if line == "" && (len(cur.Lines) == 0 || cur.Lines[len(cur.Lines)-1] == "") {
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	for i := range out {
		l := out[i].Lines
		for len(l) > 0 && l[len(l)-1] == "" {
			l = l[:len(l)-1]
		}
		out[i].Lines = l
	}
	return out
}
