package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 앱 실행 파일은 항상 이 고정된 이름으로 저장한다(zip 안에는 TTD3.4.2.exe처럼 버전이 붙어 들어있음).
// 바탕화면/작업표시줄 바로가기가 이 경로만 가리키면 업데이트로 버전이 바뀌어도 절대 깨지지 않는다.
const appExeName = "TTD.exe"

var (
	ttdAssetRe = regexp.MustCompile(`(?i)TTD.*`)
	appExeRe   = regexp.MustCompile(`(?i)^TTD.*\.exe$`)
)

// alwaysOverwrite는 이미 있어도 항상 zip의 것으로 덮어쓰는 파일들(설치 폴더 기준 상대 경로,
// 소문자, / 구분). 기본 규칙은 "없으면 추가, 있으면 그대로"(사용자 데이터 보호)지만, 이 파일들은
// 사용자 데이터가 아니라 배포본이 갱신하는 참조 데이터다 — TTD_ver_note.txt는 TTD가 업데이트
// 확인에 쓰고, map.dat/map_alias.dat는 맵 DB라 새 시즌 맵이 반영되려면 교체돼야 한다.
var alwaysOverwrite = map[string]bool{
	"ttd_ver_note.txt":   true,
	"json/map.dat":       true,
	"json/map_alias.dat": true,
}

// findVersionedAppExe는 dir에서 고정 이름(TTD.exe)이 아닌, TTD3.4.1.exe처럼 버전이 붙은 앱
// 실행 파일 중 가장 높은 버전의 이름을 찾는다(없으면 ""). 업데이터 자신(selfExe)은 제외.
func findVersionedAppExe(dir, selfExe string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var cands []zipCandidate
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.EqualFold(name, appExeName) || (selfExe != "" && strings.EqualFold(name, selfExe)) {
			continue
		}
		if appExeRe.MatchString(name) {
			cands = append(cands, zipCandidate{name: name, ver: versionFromName(name)})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	return pickHighestVersion(cands).name
}

// migrateToFixedAppExe는 고정-이름 규칙이 생기기 전부터 쓰던 폴더(TTD.exe 없이 TTD3.4.1.exe만
// 있는 폴더)를 TTD.exe로 이름을 바꿔 맞춘다. 업데이트가 실제로 적용될 때만 syncExtractedFiles가
// 이름을 고정해 주므로, "이미 최신 버전"인 사용자는 이게 없으면 영원히 TTD.exe가 생기지 않아
// 바탕화면 바로가기가 깨지고 "종료 시 TTD 실행"도 조용히 아무것도 안 하게 된다.
// 바꾼 경우 원래 파일명을, 아무것도 안 했으면 ""를 반환한다.
func migrateToFixedAppExe(dir, selfExe string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, appExeName)); err == nil {
		return "", nil
	}
	old := findVersionedAppExe(dir, selfExe)
	if old == "" {
		return "", nil
	}
	// getLocalVersion은 이 파일명(TTD3.4.1.exe)의 버전을 version_info.json보다 우선해서 읽는다.
	// 이름을 바꾸면 그 출처가 사라져 (version_info.json이 없던 폴더는) 0.0.0으로 떨어지고, 이미
	// 최신인데도 다운로드를 다시 받게 된다 — 그래서 바꾸기 전에 같은 버전을 먼저 기록해 둔다.
	if ver := versionFromName(old); ver != "0.0.0" {
		if err := saveLocalVersion(ver); err != nil {
			return old, err
		}
	}
	if err := os.Rename(filepath.Join(dir, old), filepath.Join(dir, appExeName)); err != nil {
		return old, err
	}
	return old, nil
}

// resolveAppExePath는 실제로 실행할 앱 exe 경로를 돌려준다: TTD.exe가 있으면 그것, 이름 변경이
// 실패해 버전 붙은 파일만 남아있으면 그것, 둘 다 없으면 "".
func resolveAppExePath(dir, selfExe string) string {
	fixed := filepath.Join(dir, appExeName)
	if _, err := os.Stat(fixed); err == nil {
		return fixed
	}
	if old := findVersionedAppExe(dir, selfExe); old != "" {
		return filepath.Join(dir, old)
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// syncExtractedFiles는 압축 해제된 파일들을 현재 폴더(dstDir)로 동기화한다.
// selfExe는 지금 실행 중인 업데이터 자신의 파일명으로, TTD.*로 시작해도(예: ttd_updater.exe)
// "구버전 앱 실행파일" 정리 대상에서 반드시 제외해야 한다 - 그렇지 않으면 실행 중인 자기 자신을
// 지우려다 OS에 의해 잠긴 파일 삭제 오류로 업데이트 전체가 실패한다.
func syncExtractedFiles(srcDir, dstDir, selfExe string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}

		if d.IsDir() {
			if rel == "." {
				return nil
			}
			targetDir := filepath.Join(dstDir, rel)
			if _, statErr := os.Stat(targetDir); os.IsNotExist(statErr) {
				if mkErr := os.MkdirAll(targetDir, 0755); mkErr != nil {
					return mkErr
				}
				fmt.Printf("[폴더 생성] %s\n", rel)
			}
			return nil
		}

		fileName := d.Name()
		if fileName == "version_info.json" || (selfExe != "" && strings.EqualFold(fileName, selfExe)) {
			return nil
		}

		relDir := filepath.Dir(rel)
		targetRoot := dstDir
		if relDir != "." {
			targetRoot = filepath.Join(dstDir, relDir)
		}

		isTTDAsset := ttdAssetRe.MatchString(fileName)
		isAppExe := appExeRe.MatchString(fileName)

		if isAppExe {
			dstFilePath := filepath.Join(targetRoot, appExeName)

			// 같은 폴더에 남아있는 다른 버전의 실행 파일을 정리한다(.exe끼리만 비교 -
			// ttd_price.json/TTD_ver_note.txt 같은 다른 TTD 접두 자산은 건드리지 않음).
			// 실행 중인 업데이터 자신(selfExe)은 절대 지우지 않는다.
			entries, _ := os.ReadDir(targetRoot)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				oldName := e.Name()
				if strings.EqualFold(oldName, appExeName) {
					continue
				}
				if selfExe != "" && strings.EqualFold(oldName, selfExe) {
					continue
				}
				if appExeRe.MatchString(oldName) {
					if rmErr := os.Remove(filepath.Join(targetRoot, oldName)); rmErr == nil {
						fmt.Printf("[구버전 삭제] %s\n", oldName)
					}
				}
			}

			if err := copyFile(path, dstFilePath); err != nil {
				return err
			}
			fmt.Printf("[TTD 업데이트] %s\n", filepath.Join(relDir, appExeName))
			return nil
		}

		dstFilePath := filepath.Join(targetRoot, fileName)
		if _, statErr := os.Stat(dstFilePath); os.IsNotExist(statErr) {
			if err := copyFile(path, dstFilePath); err != nil {
				return err
			}
			fmt.Printf("[신규 추가] %s\n", filepath.Join(relDir, fileName))
		} else if isTTDAsset || alwaysOverwrite[strings.ToLower(filepath.ToSlash(rel))] {
			if err := copyFile(path, dstFilePath); err != nil {
				return err
			}
			fmt.Printf("[TTD 업데이트] %s\n", filepath.Join(relDir, fileName))
		}
		return nil
	})
}
