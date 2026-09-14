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

	// ttd_updater 자기 자신의 GitHub Releases 최신 버전 확인 결과. run() 시작 부분에서 한 번
	// 채워지고, downloadURL이 비어있지 않으면 새 버전이 있다는 뜻이다.
	selfUpdateVersion     string
	selfUpdateDownloadURL string
	selfUpdateSize        int64
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.prefs = loadPrefs()
	// 체크박스/실행 옵션은 업데이트 확인이 끝나길 기다리지 않고 창이 뜨자마자 보여준다 —
	// 사용자가 다운로드 중에도 "종료 시 실행"류 설정을 미리 켜둘 수 있게 하기 위함. 실제로
	// 그 선택이 적용되는 시점(완료 버튼 활성화)은 흐름이 "ready"에 도달한 뒤다.
	runtime.EventsEmit(ctx, "init", a.prefsPayload())
	go a.run()
}

// isSafeInstallDir는 targetDir가 TTD 설치 폴더로 보이는지(또는 완전히 비어 있어 새로 설치해도
// 안전한지) 확인한다. 둘 다 아니면 — 즉 TTD와 무관해 보이는 파일들이 섞여 있으면 — false를
// 반환해 동기화를 막는다. ttd_updater.exe를 엉뚱한 폴더(예: 소스 폴더, 다운로드 폴더)에서
// 실행했을 때 그 폴더에 TTD 파일들을 쏟아붓는 사고를 막기 위한 안전장치.
//
// "기존 설치"로 인정하는 조건은 고정 이름 appExeName("TTD.exe") 하나가 아니라 appExeRe
// (`^TTD.*\.exe$`) 전체다 — 이 고정-이름 규칙이 생기기 전부터 쓰던 사용자의 폴더에는 아직
// TTD3.4.1.exe처럼 버전이 붙은 이름 그대로 남아있을 수 있고, 그것도 정당한 기존 설치다.
func isSafeInstallDir(dir string) (bool, string) {
	if _, err := os.Stat(versionFile); err == nil {
		return true, "" // version_info.json이 있으면 과거에 이 도구가 관리하던 폴더
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Sprintf("폴더를 확인할 수 없습니다: %v", err)
	}
	self := selfExeName()
	for _, e := range entries {
		// ttd_updater.exe 자신도 appExeRe(`^TTD.*\.exe$`)에 걸리므로 반드시 제외하고 검사.
		if !strings.EqualFold(e.Name(), self) && appExeRe.MatchString(e.Name()) {
			return true, "" // TTD.exe 또는 TTD3.4.1.exe처럼 버전 붙은 기존 실행 파일 발견
		}
	}
	for _, e := range entries {
		name := e.Name()
		if strings.EqualFold(name, self) || name == "updater_prefs.json" {
			continue // 업데이터 자신과 자신이 만든 설정 파일은 무시
		}
		return false, "이 폴더에 TTD와 관련 없어 보이는 파일이 있습니다. " +
			"엉뚱한 폴더에 파일이 섞이지 않도록, TTD 실행 파일(TTD.exe 또는 TTDx.x.x.exe)이 있는 " +
			"실제 설치 폴더에서 실행해 주세요."
	}
	return true, "" // 완전히 빈 폴더 — 새로 설치해도 안전
}

// cleanupStaleTempDirs는 이전 실행이 다운로드 도중 강제 종료돼 남긴 임시 폴더를 정리한다.
// 정상 종료 시에는 run()의 defer os.RemoveAll(tempDir)가 처리하지만, 프로세스가 그냥
// kill되면 defer가 실행되지 않아 %TEMP%\gdrive_update_* 폴더가 그대로 남는다 — 다음 실행
// 시작 시 한 번씩 정리해서 쌓이지 않게 한다. 실패해도 치명적이지 않으므로 에러는 무시한다.
func cleanupStaleTempDirs() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "gdrive_update_*"))
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// status는 콘솔(터미널에서 실행했을 때를 위해)과 프론트엔드 양쪽에 진행 상황을 알린다.
func (a *App) status(text string) {
	fmt.Println(text)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "status", text)
	}
}

// PrefsPayload는 체크박스/실행 옵션 화면을 그리는 데 필요한 상태다. "init"(창이 뜨자마자,
// 업데이트 확인 전)과 "ready"(업데이트 확인/적용이 끝난 뒤) 두 이벤트가 이 모양을 공유한다 —
// 옵션 자체는 업데이트 진행 상황과 무관하게 항상 같은 저장된 prefs를 반영하기 때문이다.
type PrefsPayload struct {
	LaunchOnClose         bool   `json:"launchOnClose"`
	LaunchTTD             bool   `json:"launchTTD"`
	LaunchGame            bool   `json:"launchGame"`
	GameLaunchMethod      string `json:"gameLaunchMethod"`
	GameSteamAppID        string `json:"gameSteamAppID"`
	GameClientExePath     string `json:"gameClientExePath"`
	CreateDesktopShortcut bool   `json:"createDesktopShortcut"`
}

func (a *App) prefsPayload() PrefsPayload {
	return PrefsPayload{
		LaunchOnClose:         a.prefs.LaunchOnClose,
		LaunchTTD:             a.prefs.LaunchTTDAfterUpdate,
		LaunchGame:            a.prefs.LaunchGameAfterUpdate,
		GameLaunchMethod:      a.prefs.GameLaunchMethod,
		GameSteamAppID:        a.prefs.GameSteamAppID,
		GameClientExePath:     a.prefs.GameClientExePath,
		CreateDesktopShortcut: a.prefs.CreateDesktopShortcut,
	}
}

// ReadyPayload는 업데이트 흐름이 끝났을 때(성공/이미 최신/오류) 프론트엔드로 보내는 최종 상태다.
type ReadyPayload struct {
	Message             string `json:"message"`
	AlreadyLatest       bool   `json:"alreadyLatest"`
	SelfUpdateAvailable bool   `json:"selfUpdateAvailable"`
	SelfUpdateVersion   string `json:"selfUpdateVersion"`
	PrefsPayload
}

// finish는 마지막에 한 번만 호출된다. alreadyLatest는 "이미 최신 버전이라 아무 것도 안 함"
// 케이스만 true로 표시한다 — 프론트엔드가 이 경우에만 자동 종료 카운트다운을 시작한다
// (실제 업데이트가 있었거나 오류가 났을 때는 사용자가 결과를 확인할 시간이 필요하므로 자동 종료 안 함).
func (a *App) finish(message string, alreadyLatest bool) {
	a.prefs = loadPrefs()
	runtime.EventsEmit(a.ctx, "ready", ReadyPayload{
		Message:             message,
		AlreadyLatest:       alreadyLatest,
		SelfUpdateAvailable: a.selfUpdateDownloadURL != "",
		SelfUpdateVersion:   a.selfUpdateVersion,
		PrefsPayload:        a.prefsPayload(),
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
	cleanupStaleTempDirs()

	a.status(fmt.Sprintf("ttd_updater v%s", updaterVersion))

	// ttd_updater 자신의 새 버전 확인은 TTD 본체 업데이트와 완전히 무관한 부가 기능이라,
	// 실패해도(네트워크 문제, API 오류 등) 조용히 넘어가고 본 흐름을 막지 않는다.
	if v, url, size, ok := checkSelfUpdate(); ok {
		a.selfUpdateVersion = v
		a.selfUpdateDownloadURL = url
		a.selfUpdateSize = size
		a.status(fmt.Sprintf("ttd_updater 새 버전(v%s)이 있습니다.", v))
	}

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

	// 실제로 파일을 받아 적용하기 전, targetDir가 TTD 설치 폴더가 맞는지(또는 비어 있는지)
	// 확인한다 — 엉뚱한 폴더(소스 폴더, 다운로드 폴더 등)에서 실행됐다면 여기서 중단.
	if safe, reason := isSafeInstallDir(targetDir); !safe {
		a.status(fmt.Sprintf("[중단] %s", reason))
		a.finish(reason, false)
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
// "닫을 때 바탕화면 바로가기도 만들지" 여부다(실행 여부와 별개 축). installSelfUpdateNow는
// prefs로 저장하지 않는다 — 이건 "지금 감지된 이 특정 버전을 설치할지"에 대한 일회성
// 선택이지, 매번 반복할 일반 설정이 아니기 때문이다.
func (a *App) Confirm(launchOnClose, launchTTDNow, launchGameNow, createShortcutNow, installSelfUpdateNow bool) {
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

	if installSelfUpdateNow && a.selfUpdateDownloadURL != "" {
		a.status("ttd_updater 새 버전 내려받는 중...")
		if newPath, err := downloadSelfUpdate(a.selfUpdateDownloadURL, a.selfUpdateSize); err != nil {
			a.status(fmt.Sprintf("[오류 발생] ttd_updater 업데이트 실패: %v", err))
		} else if err := installSelfUpdateAndExit(newPath); err != nil {
			a.status(fmt.Sprintf("[오류 발생] ttd_updater 교체 준비 실패: %v", err))
		}
	}

	runtime.Quit(a.ctx)
}
