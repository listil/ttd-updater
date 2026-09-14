package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Prefs는 업데이트 후 "바로 실행" 체크박스 상태와 토치라이트 실행 방식 설정을 담는다.
// LaunchOnClose와 LaunchTTDAfterUpdate/LaunchGameAfterUpdate는 서로 다른 축이다:
// 후자는 "어떤 걸 실행할지"(대상 선택), 전자는 "종료할 때 그 선택을 실제로 실행할지"를 뜻한다.
// 예를 들어 TTD를 대상으로 골라뒀어도 LaunchOnClose가 꺼져 있으면 종료 시 아무것도 실행되지 않는다.
type Prefs struct {
	LaunchOnClose         bool   `json:"launch_on_close"`
	LaunchTTDAfterUpdate  bool   `json:"launch_ttd_after_update"`
	LaunchGameAfterUpdate bool   `json:"launch_game_after_update"`
	GameLaunchMethod      string `json:"game_launch_method"` // "steam" 또는 "client"
	GameSteamAppID        string `json:"game_steam_app_id"`
	GameClientExePath     string `json:"game_client_exe_path"`
	CreateDesktopShortcut bool   `json:"create_desktop_shortcut"`
}

// loadPrefs는 설정을 불러온다. 파일이 없으면(최초 실행) 체크박스가 모두 unchecked인
// 기본값을 반환한다.
func loadPrefs() Prefs {
	p := Prefs{}
	data, err := os.ReadFile(prefsFile)
	if err != nil {
		return p
	}
	_ = json.Unmarshal(data, &p)
	return p
}

func savePrefs(p Prefs) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(prefsFile, data, 0644)
}

func launchTTD() {
	exePath := filepath.Join(targetDir, appExeName)
	if _, err := os.Stat(exePath); err != nil {
		return
	}
	cmd := exec.Command(exePath)
	cmd.Dir = targetDir
	_ = cmd.Start()
}

// createDesktopShortcut은 바탕화면에 TTD.exe로 연결되는 "TTD.lnk"를 만든다(있으면 덮어씀).
// 대상이 항상 고정된 이름(appExeName)이라 업데이트로 버전이 바뀌어도 이 바로가기는 깨지지 않는다.
// Go에 .lnk 작성 표준 라이브러리가 없어 WScript.Shell COM 객체를 PowerShell로 호출한다 —
// 별도 Go 의존성을 추가하지 않고 바이너리 용량을 늘리지 않기 위한 선택.
func createDesktopShortcut() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exePath := filepath.Join(targetDir, appExeName)
	shortcutPath := filepath.Join(homeDir, "Desktop", "TTD.lnk")

	script := fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); `+
			`$s.TargetPath = %s; $s.WorkingDirectory = %s; $s.IconLocation = %s; $s.Save()`,
		psQuote(shortcutPath), psQuote(exePath), psQuote(targetDir), psQuote(exePath),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}

// psQuote는 경로를 PowerShell 작은따옴표 문자열 리터럴로 안전하게 감싼다(내부 ' 이스케이프).
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func launchGame(p Prefs) {
	switch p.GameLaunchMethod {
	case "steam":
		if p.GameSteamAppID == "" {
			return
		}
		uri := fmt.Sprintf("steam://rungameid/%s", p.GameSteamAppID)
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", uri).Start()
	case "client":
		if p.GameClientExePath == "" {
			return
		}
		if _, err := os.Stat(p.GameClientExePath); err != nil {
			return
		}
		cmd := exec.Command(p.GameClientExePath)
		cmd.Dir = filepath.Dir(p.GameClientExePath)
		_ = cmd.Start()
	}
}
