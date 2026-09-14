package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const folderURL = "https://drive.google.com/drive/folders/1vqdXS4oNRkVy1r8-1DkWVC5mvvsUhreF"

var (
	targetDir   string
	versionFile string
	prefsFile   string
)

func init() {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	targetDir = wd
	versionFile = filepath.Join(targetDir, "version_info.json")
	prefsFile = filepath.Join(targetDir, "updater_prefs.json")
}

// App은 Wails 프론트엔드(frontend/dist)에 바인딩되는 백엔드 진입점이다.
// startup에서 백그라운드로 업데이트를 진행하며 "status"/"ready" 이벤트로 진행 상황을 알리고,
// 나머지 메서드는 완료 후 체크박스 화면에서 프론트엔드가 호출한다.
type App struct {
	ctx   context.Context
	prefs Prefs
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.run()
}

// status는 콘솔(터미널에서 실행했을 때를 위해)과 프론트엔드 양쪽에 진행 상황을 알린다.
func (a *App) status(text string) {
	fmt.Println(text)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "status", text)
	}
}

// ReadyPayload는 업데이트 흐름이 끝났을 때(성공/이미 최신/오류) 프론트엔드로 보내는 최종 상태다.
type ReadyPayload struct {
	Message               string `json:"message"`
	AlreadyLatest         bool   `json:"alreadyLatest"`
	LaunchOnClose         bool   `json:"launchOnClose"`
	LaunchTTD             bool   `json:"launchTTD"`
	LaunchGame            bool   `json:"launchGame"`
	GameLaunchMethod      string `json:"gameLaunchMethod"`
	GameSteamAppID        string `json:"gameSteamAppID"`
	GameClientExePath     string `json:"gameClientExePath"`
	CreateDesktopShortcut bool   `json:"createDesktopShortcut"`
}

// finish는 마지막에 한 번만 호출된다. alreadyLatest는 "이미 최신 버전이라 아무 것도 안 함"
// 케이스만 true로 표시한다 — 프론트엔드가 이 경우에만 자동 종료 카운트다운을 시작한다
// (실제 업데이트가 있었거나 오류가 났을 때는 사용자가 결과를 확인할 시간이 필요하므로 자동 종료 안 함).
func (a *App) finish(message string, alreadyLatest bool) {
	a.prefs = loadPrefs()
	runtime.EventsEmit(a.ctx, "ready", ReadyPayload{
		Message:               message,
		AlreadyLatest:         alreadyLatest,
		LaunchOnClose:         a.prefs.LaunchOnClose,
		LaunchTTD:             a.prefs.LaunchTTDAfterUpdate,
		LaunchGame:            a.prefs.LaunchGameAfterUpdate,
		GameLaunchMethod:      a.prefs.GameLaunchMethod,
		GameSteamAppID:        a.prefs.GameSteamAppID,
		GameClientExePath:     a.prefs.GameClientExePath,
		CreateDesktopShortcut: a.prefs.CreateDesktopShortcut,
	})
}

type zipCandidate struct {
	id, name, ver string
}

func pickHighestVersion(cands []zipCandidate) zipCandidate {
	sort.Slice(cands, func(i, j int) bool {
		pi, _ := parseVersion(cands[i].ver)
		pj, _ := parseVersion(cands[j].ver)
		for k := 0; k < 3; k++ {
			if pi[k] != pj[k] {
				return pi[k] > pj[k]
			}
		}
		return false
	})
	return cands[0]
}

// run은 버전 확인 -> (필요 시) 다운로드/적용까지의 업데이트 흐름 전체를 수행하고,
// 어느 경로로 끝나든 마지막에 a.finish()를 호출해 프론트엔드를 체크박스 화면으로 전환시킨다.
func (a *App) run() {
	a.status("=== 구글 드라이브 업데이트 확인 중 ===")
	localVer := getLocalVersion()
	a.status(fmt.Sprintf("현재 로컬 버전: %s", localVer))

	// 1. 파일 다운로드 없이 서버의 최신 버전 미리 검사
	remoteZipName, remoteVer := getRemoteZipInfoWithoutDownload(folderURL)
	if remoteVer != "" {
		a.status(fmt.Sprintf("드라이브 최신 파일 감지: %s (버전: %s)", remoteZipName, remoteVer))
		if !isNewerVersion(remoteVer, localVer) {
			a.status(">> 이미 최신 버전을 사용 중입니다.")
			a.finish("이미 최신 버전을 사용 중입니다.", true)
			return
		}
		a.status(fmt.Sprintf(">> 새 버전(%s)이 발견되어 다운로드를 시작합니다!", remoteVer))
	}

	client := newHTTPClient(60 * time.Second)

	tempDir, err := os.MkdirTemp("", "gdrive_update_")
	if err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 확인 중 오류가 발생했습니다.", false)
		return
	}
	defer os.RemoveAll(tempDir)

	// 2. 폴더 목록에서 최신 ZIP 하나만 찾아 그 파일만 다운로드 (전체 폴더 다운로드 불필요)
	folderID := extractFolderID(folderURL)
	entries, err := listDriveFolder(folderID, client)
	if err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 확인 중 오류가 발생했습니다.", false)
		return
	}

	var zipEntries []zipCandidate
	for _, e := range entries {
		if !strings.HasSuffix(strings.ToLower(e.name), ".zip") {
			continue
		}
		zipEntries = append(zipEntries, zipCandidate{id: e.id, name: e.name, ver: versionFromName(e.name)})
	}
	if len(zipEntries) == 0 {
		a.status("드라이브 폴더 내에 ZIP 파일이 없습니다.")
		a.finish("드라이브 폴더 내에 ZIP 파일을 찾지 못했습니다.", false)
		return
	}
	target := pickHighestVersion(zipEntries)

	// 사전 검사를 안 거쳤던 경우 여기서 최종 체크
	if remoteVer == "" && !isNewerVersion(target.ver, localVer) {
		a.status(">> 이미 최신 버전을 사용 중입니다.")
		a.finish("이미 최신 버전을 사용 중입니다.", true)
		return
	}

	a.status(fmt.Sprintf("파일 다운로드 중... (%s)", target.name))
	zipPath := filepath.Join(tempDir, target.name)
	if err := downloadDriveFile(target.id, zipPath, client); err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 다운로드 중 오류가 발생했습니다.", false)
		return
	}

	// 3. 압축 해제 및 파일 반영
	a.status("압축 해제 및 파일 적용 중...")
	extractDir := filepath.Join(tempDir, "extracted")
	if err := unzip(zipPath, extractDir); err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 적용 중 오류가 발생했습니다.", false)
		return
	}

	if err := syncExtractedFiles(extractDir, targetDir, selfExeName()); err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 적용 중 오류가 발생했습니다.", false)
		return
	}

	saveVer := remoteVer
	if saveVer == "" {
		saveVer = target.ver
	}
	if saveVer != "0.0.0" {
		_ = saveLocalVersion(saveVer)
	}

	a.status("[성공] 업데이트가 완료되었습니다!")
	a.finish("업데이트가 완료되었습니다.", false)
}

// 공식 클라이언트의 통상적인 설치 경로. 사용자가 설치 위치를 바꿨을 수도 있으므로
// 무조건 신뢰하지 않고, DefaultClientExePath에서 실제로 그 자리에 파일이 있는지
// 확인한 뒤에만 프론트엔드에 "제안값"으로 넘긴다 — Steam App ID처럼 무조건 맞는 값이 아니다.
const defaultClientExePath = `C:\Program Files (x86)\Torchlight Infinite\Engine\Binaries\Win64\TorchlightLauncher.exe`

// DefaultClientExePath는 통상적인 설치 경로에 실제로 실행 파일이 있으면 그 경로를,
// 없으면 빈 문자열을 반환한다 — 프론트엔드는 사용자가 아직 경로를 고르지 않았을 때만
// 이 값을 "찾아보기 전 제안"으로 미리 채워 넣는 데 쓴다.
func (a *App) DefaultClientExePath() string {
	if _, err := os.Stat(defaultClientExePath); err != nil {
		return ""
	}
	return defaultClientExePath
}

// BrowseForGameExe는 토치라이트 공식 클라이언트 exe를 고르는 네이티브 파일 선택 대화상자를 연다.
func (a *App) BrowseForGameExe() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "토치라이트 실행 파일 선택",
		Filters: []runtime.FileFilter{{DisplayName: "실행 파일 (*.exe)", Pattern: "*.exe"}},
	})
	if err != nil {
		return ""
	}
	return path
}

// SaveGameSetup은 토치라이트 실행 방식(Steam App ID 또는 클라이언트 경로)을 저장한다.
// 값은 사용자마다 다르므로 여기서 추측/기본값 지정 없이 프론트엔드가 입력받은 그대로 저장한다.
func (a *App) SaveGameSetup(method, steamAppID, clientExePath string) {
	a.prefs.GameLaunchMethod = method
	a.prefs.GameSteamAppID = steamAppID
	a.prefs.GameClientExePath = clientExePath
	_ = savePrefs(a.prefs)
}

// Confirm은 체크박스 화면의 "완료" 버튼(또는 자동 종료)에서 호출된다: 선택 상태를 다음
// 실행을 위해 저장하고 창을 닫는다. launchOnClose와 launchTTDNow/launchGameNow는 서로
// 다른 축이다 — launchTTDNow/launchGameNow는 "TTD/게임 중 무엇을 대상으로 할지" 선택이고,
// launchOnClose는 "이번에 닫을 때 그 선택을 실제로 실행할지"이다. 후자가 꺼져 있으면
// 대상이 체크돼 있어도 아무 것도 실행하지 않는다. createShortcutNow는 둘과 무관하게
// "닫을 때 바탕화면 바로가기도 만들지" 여부다(실행 여부와 별개 축).
func (a *App) Confirm(launchOnClose, launchTTDNow, launchGameNow, createShortcutNow bool) {
	a.prefs.LaunchOnClose = launchOnClose
	a.prefs.LaunchTTDAfterUpdate = launchTTDNow
	a.prefs.LaunchGameAfterUpdate = launchGameNow
	a.prefs.CreateDesktopShortcut = createShortcutNow
	_ = savePrefs(a.prefs)

	if launchOnClose {
		if launchTTDNow {
			launchTTD()
		}
		if launchGameNow && a.prefs.GameLaunchMethod != "" {
			launchGame(a.prefs)
		}
	}

	if createShortcutNow {
		_ = createDesktopShortcut()
	}

	runtime.Quit(a.ctx)
}
