package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var localVersionRe = regexp.MustCompile(`(?i)TTD[_-]?v?(\d+\.\d+\.\d+)`)

// getLocalVersion은 TARGET_DIR에서 TTDx.x.x 형태의 파일명을 찾아 버전을 가져온다.
// 실행 파일이 appExeName("TTD.exe")으로 고정된 뒤로는 파일명에 버전이 없으므로
// 보통 version_info.json이 실질적인 버전 출처가 된다.
func getLocalVersion() string {
	entries, err := os.ReadDir(targetDir)
	if err == nil {
		for _, e := range entries {
			if m := localVersionRe.FindStringSubmatch(e.Name()); m != nil {
				return m[1]
			}
		}
	}

	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "0.0.0"
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.Version == "" {
		return "0.0.0"
	}
	return v.Version
}

// saveLocalVersion은 업데이트 완료 후 로컬 버전 정보를 저장한다.
func saveLocalVersion(version string) error {
	data, err := json.MarshalIndent(map[string]string{"version": version}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(versionFile, data, 0644)
}

// parseVersion은 "1.2.3" 형태를 (1,2,3)으로 변환한다. 실패 시 ok=false.
func parseVersion(v string) (parts [3]int, ok bool) {
	segs := strings.Split(v, ".")
	if len(segs) != 3 {
		return parts, false
	}
	for i, s := range segs {
		n, err := strconv.Atoi(s)
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// isNewerVersion은 버전 비교(예: 1.0.3 > 1.0.2)를 수행한다.
// 파싱에 실패하면(원본 Python의 ValueError 폴백과 동일하게) 단순 문자열 불일치로 판단한다.
func isNewerVersion(newVer, currentVer string) bool {
	np, nok := parseVersion(newVer)
	cp, cok := parseVersion(currentVer)
	if !nok || !cok {
		return newVer != currentVer
	}
	for i := 0; i < 3; i++ {
		if np[i] != cp[i] {
			return np[i] > cp[i]
		}
	}
	return false
}

func selfExeName() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(exe)
}
