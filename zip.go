package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// unzip은 zipPath를 destDir 아래에 압축 해제한다(디렉터리 구조 보존).
func unzip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		targetPath := filepath.Join(destDir, f.Name)

		// zip slip 방지: 압축 해제 경로가 destDir 밖으로 나가지 않도록 확인.
		if !strings.HasPrefix(targetPath, filepath.Clean(destDir)+string(os.PathSeparator)) &&
			targetPath != filepath.Clean(destDir) {
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// checkTTDZip은 사용자가 직접 고른 zip이 TTD 배포본인지 적용 전에 확인한다 — 열리는 zip인지
// (브라우저에서 받다가 끊긴 파일은 여기서 걸린다), 안에 TTD 실행 파일이 있는지.
func checkTTDZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("zip 파일을 열 수 없습니다. 받다가 끊겼을 수 있으니 다시 받아 주세요. (%v)", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if isAppExeFile(filepath.Base(f.Name)) {
			return nil
		}
	}
	return fmt.Errorf("선택한 zip 안에 TTD 실행 파일(TTD*.exe)이 없습니다. TTD 배포 zip이 맞는지 확인해 주세요")
}
