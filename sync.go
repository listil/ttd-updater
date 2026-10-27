package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// 앱 실행 파일은 항상 이 고정된 이름으로 저장한다(zip 안에는 TTD3.4.2.exe처럼 버전이 붙어 들어있음).
// 바탕화면/작업표시줄 바로가기가 이 경로만 가리키면 업데이트로 버전이 바뀌어도 절대 깨지지 않는다.
const appExeName = "TTD.exe"

var (
	ttdAssetRe   = regexp.MustCompile(`(?i)TTD.*`)
	appExeRe     = regexp.MustCompile(`(?i)^TTD.*\.exe$`)
	updaterExeRe = regexp.MustCompile(`(?i)^ttd_updater.*\.exe$`)
)

// isUpdaterExe는 이 업데이터 자신의 실행 파일(ttd_updater.exe, 다운로드 중복 이름인
// "ttd_updater (1).exe" 등)인지 판별한다. 업데이터는 GitHub Releases로 따로 자체 업데이트하므로
// TTD zip에 들어 있는 사본은 설치하지 않는다.
func isUpdaterExe(name string) bool {
	return updaterExeRe.MatchString(name)
}

// isAppExeFile은 TTD 본체 실행 파일(TTD.exe, TTD3.4.6.exe 등)인지 판별한다. ttd_updater.exe도
// 이름이 "ttd"로 시작해 appExeRe에 걸리므로 반드시 빼야 한다 — 빼지 않으면 실행 중인 업데이터의
// 이름이 달라 selfExe 제외가 안 먹는 경우 zip의 ttd_updater.exe가 TTD.exe를 덮어쓴다.
func isAppExeFile(name string) bool {
	return appExeRe.MatchString(name) && !isUpdaterExe(name)
}

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
		if isAppExeFile(name) {
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

// errSharingViolation은 Windows ERROR_SHARING_VIOLATION(32) — 실행 중인 exe를 쓰기용으로
// 열면 나는 "다른 프로세스가 파일을 사용 중" 오류다. 읽기 전용 속성 때문에 나는
// ERROR_ACCESS_DENIED(5)와 구별하려고 이 코드만 "실행 중"으로 본다.
const errSharingViolation = syscall.Errno(32)

// runningAppExe는 dir의 TTD 실행 파일(TTD.exe, TTD3.4.1.exe 등) 중 지금 실행 중이라 잠겨 있는
// 것의 이름을 돌려준다(없으면 ""). 쓰기용으로 열어 보기만 하고 아무것도 쓰지 않는다(잘라내기 없음).
// 실행 중인 exe는 덮어쓸 수 없어 syncExtractedFiles가 중간에 실패하므로, 파일을 하나라도 바꾸기
// 전에 이걸로 먼저 확인한다.
func runningAppExe(dir, selfExe string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isAppExeFile(name) || (selfExe != "" && strings.EqualFold(name, selfExe)) {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY, 0)
		if err == nil {
			f.Close()
			continue
		}
		if errors.Is(err, errSharingViolation) {
			return name
		}
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
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	// Close 오류(디스크 부족 시 마지막 버퍼 flush 실패 등)도 실패로 본다 — 무시하면 잘린 파일이
	// 성공한 것처럼 남는다.
	return out.Close()
}

// 업데이트로 교체되는 구버전 앱 실행 파일을 보관하는 하위 폴더(설치 폴더 기준).
// findVersionedAppExe/getLocalVersion은 설치 폴더 바로 아래만 보므로 여기 둔 백업은 버전 판별에
// 끼어들지 않는다.
const backupDirName = "backup"

// backupDirName 자리에 같은 이름의 "파일"이 있는 등 폴더를 만들 수 없을 때 대신 쓰는 폴더.
const backupDirFallback = "backup_ttd"

// ensureBackupDir는 쓸 수 있는 백업 폴더 경로를 돌려준다(backup/, 안 되면 backup_ttd/).
func ensureBackupDir(dir string) (string, error) {
	var lastErr error
	for _, name := range []string{backupDirName, backupDirFallback} {
		bdir := filepath.Join(dir, name)
		if err := os.MkdirAll(bdir, 0755); err != nil {
			lastErr = err
			continue
		}
		return bdir, nil
	}
	return "", lastErr
}

// timestampedName은 TTD3.4.2.exe → TTD3.4.2_20261027_153000.exe. 같은 이름의 백업이 잠겨
// 있거나 내용이 다를 때, 기존 것을 건드리지 않고 나란히 저장하는 용도.
func timestampedName(name string) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s_%s%s", strings.TrimSuffix(name, ext), time.Now().Format("20060102_150405"), ext)
}

// sameContent는 두 파일이 크기와 SHA-256까지 같은지 본다(둘 중 하나라도 못 읽으면 false).
// 같은 버전을 다시 설치할 때 이미 있는 백업을 다시 쓰지 않으려고 쓴다.
func sameContent(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil || sa.Size() != sb.Size() {
		return false
	}
	hash := func(p string) ([]byte, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return nil, err
		}
		return h.Sum(nil), nil
	}
	ha, errA := hash(a)
	hb, errB := hash(b)
	return errA == nil && errB == nil && bytes.Equal(ha, hb)
}

// copyFileAtomic은 dst.tmp에 끝까지 쓴 뒤 dst로 바꿔치기한다 — 디스크 부족 등으로 중간에
// 실패해도 잘린 exe가 dst 이름으로 남지 않는다. 읽기 전용 dst는 속성을 풀고 덮어쓴다.
func copyFileAtomic(src, dst string) error {
	tmp := dst + ".tmp"
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(dst, 0666) // 읽기 전용 해제(없거나 실패해도 아래 Rename이 판단)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// backupExe는 src를 백업 폴더에 name으로 복사하고 실제 저장된 경로를 돌려준다.
//   - 같은 내용의 백업이 이미 있으면(같은 버전 재설치) 그대로 둔다.
//   - 같은 이름이 잠겨 있거나(backup/ 안의 exe를 직접 실행 중) 덮어쓸 수 없으면 시각을 붙인
//     이름으로 따로 저장한다.
func backupExe(dir, src, name string) (string, error) {
	bdir, err := ensureBackupDir(dir)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(bdir, name)
	if sameContent(src, dst) {
		return dst, nil
	}
	if err := copyFileAtomic(src, dst); err == nil {
		return dst, nil
	}
	alt := filepath.Join(bdir, timestampedName(name))
	if err := copyFileAtomic(src, alt); err != nil {
		return "", err
	}
	return alt, nil
}

// moveStaleExeToBackup은 설치 폴더에 남은 버전 붙은 구버전 exe(TTD3.4.0.exe)를 백업 폴더로
// 옮긴다. 옮기지 못해도 설치 폴더에는 절대 남기지 않는다 — getLocalVersion은 설치 폴더의
// TTDx.y.z 파일명을 version_info.json보다 우선하므로, 남아 있으면 구버전으로 오인해 매번
// 업데이트를 다시 받는다. 그래서 마지막 수단은 (예전 동작대로) 삭제다.
func moveStaleExeToBackup(dir, name string) {
	src := filepath.Join(dir, name)
	if bdir, err := ensureBackupDir(dir); err == nil {
		dst := filepath.Join(bdir, name)
		if sameContent(src, dst) {
			if os.Remove(src) == nil {
				fmt.Printf("[구버전 정리] %s (이미 %s에 백업됨)\n", name, relToDir(dir, dst))
				return
			}
		} else {
			if _, statErr := os.Stat(dst); statErr == nil {
				dst = filepath.Join(bdir, timestampedName(name)) // 다른 내용의 같은 이름 백업은 보존
			}
			if os.Rename(src, dst) == nil {
				fmt.Printf("[구버전 백업] %s -> %s\n", name, relToDir(dir, dst))
				return
			}
		}
	}
	if err := os.Remove(src); err == nil {
		fmt.Printf("[구버전 삭제] %s (백업 폴더로 옮기지 못함)\n", name)
	} else {
		fmt.Printf("[경고] 구버전 %s 정리 실패(TTD 실행 중이면 종료 후 다시 시도): %v\n", name, err)
	}
}

func relToDir(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil {
		return r
	}
	return p
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
		// zip에 함께 들어 있는 업데이터 사본(ttd_updater.exe)은 설치하지 않는다 — 실행 중인 자기
		// 자신이라 덮어쓸 수도 없고, 업데이터 버전 관리는 GitHub Releases 자체 업데이트가 맡는다.
		// selfExe 비교는 업데이터를 아예 다른 이름(예: "업데이터.exe")으로 바꿔 실행한 경우용.
		if fileName == "version_info.json" || isUpdaterExe(fileName) ||
			(selfExe != "" && strings.EqualFold(fileName, selfExe)) {
			return nil
		}

		relDir := filepath.Dir(rel)
		targetRoot := dstDir
		if relDir != "." {
			targetRoot = filepath.Join(dstDir, relDir)
		}

		// json/ 아래의 ttd_* 파일(ttd_resume_snapshot.json, ttd_log_state.json 등)은 TTD가 실행 중에
		// 만드는 세션/복구 상태라, 이름이 TTD로 시작해도 "배포 자산 덮어쓰기" 대상에서 뺀다 — 혹시
		// zip에 초기본이 들어가더라도 업데이트할 때마다 사용자의 진행 중 세션이 지워지지 않게.
		// (pet/ttd_1.webp, sound/ttd1.wav 같은 다른 하위 폴더의 TTD 자산은 계속 덮어쓴다.)
		isStateDir := strings.EqualFold(strings.SplitN(filepath.ToSlash(rel), "/", 2)[0], "json")
		isTTDAsset := ttdAssetRe.MatchString(fileName) && !isStateDir
		isAppExe := isAppExeFile(fileName)

		if isAppExe {
			dstFilePath := filepath.Join(targetRoot, appExeName)

			// 같은 폴더에 남아있는 다른 버전의 실행 파일을 정리한다(.exe끼리만 비교 -
			// ttd_price.json/TTD_ver_note.txt 같은 다른 TTD 접두 자산은 건드리지 않음).
			// 실행 중인 업데이터 자신(selfExe)은 절대 지우지 않는다.
			// 지우지 않고 backup/ 폴더로 옮겨, 새 버전에 문제가 있으면 사용자가 되돌릴 수 있게 한다.
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
				if isAppExeFile(oldName) {
					moveStaleExeToBackup(targetRoot, oldName)
				}
			}

			// zip에 들어 있던 버전 붙은 원본(TTD3.4.2.exe)도 이름 그대로 backup/에 남겨, 설치된
			// TTD.exe가 어느 버전인지와 무관하게 각 버전의 원본 파일을 보관한다. 백업은 부가 기능이라
			// 실패해도 경고만 남기고 업데이트(TTD.exe 교체)는 계속한다.
			if saved, bErr := backupExe(targetRoot, path, fileName); bErr != nil {
				fmt.Printf("[경고] %s 백업 실패(업데이트는 계속): %v\n", fileName, bErr)
			} else {
				fmt.Printf("[신규 버전 백업] %s -> %s\n", fileName, relToDir(targetRoot, saved))
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
