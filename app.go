package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// folderURL은 예전 배포처인 구글 드라이브 폴더다. 지금은 GitHub(ttdRepo)에 접속할 수 없을 때만 쓴다.
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
	ctx context.Context

	// prefs는 run() 고루틴(finish)과 프론트엔드 바인딩 호출(SetTheme/SaveGameSetup/Confirm)이
	// 서로 다른 고루틴에서 동시에 읽고 쓰므로 prefsMu로 보호한다.
	prefsMu sync.Mutex
	prefs   Prefs

	domReadyOnce sync.Once

	// 확인은 끝났고 사용자의 "업데이트" 클릭(StartUpdate)을 기다리는 업데이트. nil이면 없음.
	updateMu sync.Mutex
	pending  *pendingUpdate

	// ttd_updater 자기 자신의 GitHub Releases 최신 버전 확인 결과. run() 시작 부분에서 한 번
	// 채워지고, downloadURL이 비어있지 않으면 새 버전이 있다는 뜻이다.
	selfUpdateVersion     string
	selfUpdateDownloadURL string
	selfUpdateSize        int64
	selfCheckDone         chan struct{} // run의 런처 자체 업데이트 확인이 끝나면 닫힌다
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.prefs = loadPrefs()
}

// domReady는 페이지(app.js)가 로드돼 이벤트 리스너가 등록된 뒤에 호출된다. startup은 index.html
// 로드 *전*에 불리기 때문에 거기서 보낸 이벤트("init", 첫 status 줄 등)는 화면이 받지 못하고
// 버려진다 — 그래서 저장된 테마/체크박스가 업데이트가 끝난 "ready" 때에야 반영돼, 다운로드가 긴
// 업데이트에서는 테마가 도중에 바뀌어 보였다. 새로고침 등으로 다시 불려도 업데이트는 한 번만.
func (a *App) domReady(ctx context.Context) {
	a.domReadyOnce.Do(func() {
		// 체크박스/실행 옵션은 업데이트 확인이 끝나길 기다리지 않고 창이 뜨자마자 보여준다 —
		// 사용자가 다운로드 중에도 "종료 시 실행"류 설정을 미리 켜둘 수 있게 하기 위함. 실제로
		// 그 선택이 적용되는 시점(완료 버튼 활성화)은 흐름이 "ready"에 도달한 뒤다.
		runtime.EventsEmit(ctx, "init", a.prefsPayload())
		go a.run()
	})
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
		// ttd_updater.exe 자신도 appExeRe(`^TTD.*\.exe$`)에 걸리므로 반드시 제외하고 검사
		// (isAppExeFile이 ttd_updater*.exe를 빼고, self 비교는 아예 다른 이름으로 바꾼 경우용).
		if !strings.EqualFold(e.Name(), self) && isAppExeFile(e.Name()) {
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
	Theme                 string `json:"theme"`
	SkipUpdateVersion     string `json:"skipUpdateVersion"`
}

func (a *App) prefsPayload() PrefsPayload {
	a.prefsMu.Lock()
	defer a.prefsMu.Unlock()
	return PrefsPayload{
		LaunchOnClose:         a.prefs.LaunchOnClose,
		LaunchTTD:             a.prefs.LaunchTTDAfterUpdate,
		LaunchGame:            a.prefs.LaunchGameAfterUpdate,
		GameLaunchMethod:      a.prefs.GameLaunchMethod,
		GameSteamAppID:        a.prefs.GameSteamAppID,
		GameClientExePath:     a.prefs.GameClientExePath,
		CreateDesktopShortcut: a.prefs.CreateDesktopShortcut,
		Theme:                 a.prefs.Theme,
		SkipUpdateVersion:     a.prefs.SkipUpdateVersion,
	}
}

// ReadyPayload는 업데이트 흐름이 끝났을 때(성공/이미 최신/오류) 프론트엔드로 보내는 최종 상태다.
type ReadyPayload struct {
	Message         string `json:"message"`
	AlreadyLatest   bool   `json:"alreadyLatest"`
	Error           bool   `json:"error"`
	UpdateAvailable bool   `json:"updateAvailable"` // 새 버전 확인됨, 사용자 확정 대기 중
	UpdateVersion   string `json:"updateVersion"`
	Blocked         bool   `json:"blocked"` // TTD 실행 중이라 업데이트를 못 해 대기로 되돌림
	// DownloadFailed면 드라이브에서 받지 못해 대기로 되돌린 상태다 — 프론트엔드가 ManualURL(그 zip의
	// 드라이브 페이지)로 "업데이트 수동으로 받기"와 "받은 zip으로 업데이트" 버튼을 보여준다.
	DownloadFailed      bool   `json:"downloadFailed"`
	ManualURL           string `json:"manualUrl"`
	SelfUpdateAvailable bool   `json:"selfUpdateAvailable"`
	SelfUpdateVersion   string `json:"selfUpdateVersion"`
	PrefsPayload
}

// finish는 마지막에 한 번만 호출된다. alreadyLatest는 "이미 최신 버전이라 아무 것도 안 함"
// 케이스만 true로 표시한다 — 프론트엔드가 이 경우에만 자동 종료 카운트다운을 시작한다
// (실제 업데이트가 있었거나 오류가 났을 때는 사용자가 결과를 확인할 시간이 필요하므로 자동 종료 안 함).
// isError는 화면의 성공/실패 표시용 — 메시지 문구로 추측하지 않도록 호출하는 쪽이 명시한다.
func (a *App) finish(message string, alreadyLatest, isError bool) {
	a.emitReady(ReadyPayload{Message: message, AlreadyLatest: alreadyLatest, Error: isError})
}

// finishWithUpdate는 "새 버전이 있지만 아직 받지 않음" 상태로 화면을 넘긴다 — 프론트엔드는
// 설치 체크박스와 "업데이트" 버튼을 보여주고, 사용자가 누르면 StartUpdate를 호출한다.
func (a *App) finishWithUpdate(version string) {
	a.emitReady(ReadyPayload{
		Message:         fmt.Sprintf("새 버전 v%s을 설치할 수 있습니다.", version),
		UpdateAvailable: true,
		UpdateVersion:   version,
	})
}

func (a *App) emitReady(p ReadyPayload) {
	if a.selfCheckDone != nil {
		<-a.selfCheckDone // 런처 자체 업데이트 확인 결과가 나온 뒤에 보낸다
	}
	p.SelfUpdateAvailable = a.selfUpdateDownloadURL != ""
	p.SelfUpdateVersion = a.selfUpdateVersion
	p.PrefsPayload = a.prefsPayload()
	runtime.EventsEmit(a.ctx, "ready", p)
}

// NotesPayload는 업데이트 내역 패널 데이터다. Sections가 nil이면 불러오기 실패.
// 각 섹션의 IsNew(이번에 받게 될 버전인지)는 백엔드가 isNewerVersion으로 계산해 넣는다.
type NotesPayload struct {
	Sections []NoteSection `json:"sections"`
}

// emitVersions는 헤더의 "현재 → 최신" 버전 표시를 갱신한다(remote는 모르면 "").
func (a *App) emitVersions(local, remote string) {
	runtime.EventsEmit(a.ctx, "versions", map[string]string{"local": local, "remote": remote})
}

// zipCandidate는 받을 TTD zip 하나다. GitHub 릴리즈에서 찾았으면 url/size/page가, 구글 드라이브
// (대비용)에서 찾았으면 id가 채워진다.
type zipCandidate struct {
	id, name, ver string
	url, page     string
	size          int64
}

// manualURL은 사용자가 브라우저로 직접 받을 페이지다("업데이트 수동으로 받기").
func (z zipCandidate) manualURL() string {
	if z.id != "" {
		return "https://drive.google.com/file/d/" + z.id + "/view"
	}
	if z.page != "" {
		return z.page
	}
	return ttdReleasesPage
}

// download는 zip을 destPath로 받는다 — GitHub 첨부 파일이면 바로, 드라이브면 확인 페이지 처리까지.
func (z zipCandidate) download(destPath string, progress func(written, total int64)) error {
	if z.id != "" {
		return downloadDriveFile(z.id, destPath, newDownloadClient(), progress)
	}
	return downloadURLFile(z.url, destPath, z.size, newDownloadClient(), progress)
}

// latestDriveZip은 대비용 배포처인 구글 드라이브 폴더에서 버전이 가장 높은 zip을 찾는다.
func latestDriveZip(client *http.Client) (zipCandidate, error) {
	entries, err := listDriveFolder(extractFolderID(folderURL), client)
	if err != nil {
		return zipCandidate{}, err
	}
	var zips []zipCandidate
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.name), ".zip") {
			zips = append(zips, zipCandidate{id: e.id, name: e.name, ver: versionFromName(e.name)})
		}
	}
	if len(zips) == 0 {
		return zipCandidate{}, fmt.Errorf("드라이브 폴더에서 ZIP 파일을 찾지 못했습니다")
	}
	return pickHighestVersion(zips), nil
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

// run은 업데이트 "확인"까지만 수행한다. 새 버전이 있으면 받지 않고 finishWithUpdate로 사용자
// 확정을 기다리며(실제 다운로드/적용은 StartUpdate → applyUpdate), 그 외에는 a.finish()로 끝낸다.
func (a *App) run() {
	cleanupStaleTempDirs()

	a.status(fmt.Sprintf("ttd_updater v%s", updaterVersion))

	// 버전 붙은 TTD3.x.x.exe만 있는 옛 폴더를 고정 이름 TTD.exe로 맞춘다 — 업데이트가 없어도
	// 바로가기/"종료 시 TTD 실행"이 TTD.exe를 찾을 수 있게.
	if old, err := migrateToFixedAppExe(targetDir, selfExeName()); err != nil {
		a.status(fmt.Sprintf("[경고] %s → %s 이름 변경 실패(TTD 실행 중이면 종료 후 다시 시도): %v", old, appExeName, err))
	} else if old != "" {
		a.status(fmt.Sprintf("%s → %s 로 이름을 고정했습니다.", old, appExeName))
	}

	// ttd_updater 자신의 새 버전 확인은 TTD 본체 업데이트와 완전히 무관한 부가 기능이라,
	// 실패해도(네트워크 문제, API 오류 등) 조용히 넘어가고 본 흐름을 막지 않는다.
	// TTD 확인과 동시에 돌려, GitHub가 느릴 때 두 확인의 대기 시간이 더해지지 않게 한다. 결과 필드는
	// emitReady가 selfCheckDone을 기다린 뒤에만 읽는다.
	a.selfCheckDone = make(chan struct{})
	go func() {
		defer close(a.selfCheckDone)
		if info, ok := checkSelfUpdate(); ok {
			a.selfUpdateVersion = info.Version
			a.selfUpdateDownloadURL = info.DownloadURL
			a.selfUpdateSize = info.Size
			a.status(fmt.Sprintf("ttd_updater 새 버전(v%s)이 있습니다.", info.Version))
			runtime.EventsEmit(a.ctx, "selfNotes", info.Notes)
		}
	}()

	a.status("=== TTD 업데이트 확인 중 ===")
	localVer := getLocalVersion()
	a.status(fmt.Sprintf("현재 로컬 버전: %s", localVer))
	a.emitVersions(localVer, "")

	// 업데이트 내역 패널은 업데이트 확인/다운로드와 무관하게 병렬로 채운다 — 이미 최신이어도
	// 최근 내역을 보여주고, 업데이트가 있으면 다운로드가 도는 동안 읽을 수 있게. 다만 NEW 표시는
	// "실제로 받을 수 있는 버전"까지만 붙여야 하므로(노트가 zip보다 먼저 갱신될 수 있음), 배포처의
	// 최신 버전이 확인될 때까지(또는 run이 끝날 때까지) 기다렸다가 계산한다.
	remoteKnown := make(chan string, 1)
	var remoteOnce sync.Once
	setRemote := func(v string) { remoteOnce.Do(func() { remoteKnown <- v }) }
	defer setRemote("")
	go func() {
		sections := fetchReleaseNotes()
		markNewSections(sections, localVer, <-remoteKnown)
		runtime.EventsEmit(a.ctx, "notes", NotesPayload{Sections: sections})
	}()

	// 배포처는 GitHub Releases다. GitHub에 접속할 수 없을 때만(방화벽, API 제한 등) 예전 배포처인
	// 구글 드라이브 폴더에서 찾는다.
	client := newHTTPClient(20 * time.Second)
	target, err := latestGitHubZip(client)
	if err == nil {
		a.status(fmt.Sprintf("GitHub 최신 릴리즈: %s (버전: %s)", target.name, target.ver))
	} else {
		a.status(fmt.Sprintf("[알림] GitHub 확인 실패 — 구글 드라이브에서 확인합니다. (%v)", err))
		target, err = latestDriveZip(client)
		if err != nil {
			a.status(fmt.Sprintf("[오류 발생] %v", err))
			a.finish("업데이트 확인 중 오류가 발생했습니다.", false, true)
			return
		}
		a.status(fmt.Sprintf("드라이브 최신 파일: %s (버전: %s)", target.name, target.ver))
	}
	setRemote(target.ver)
	a.emitVersions(localVer, target.ver)

	if !isNewerVersion(target.ver, localVer) {
		a.status(">> 이미 최신 버전을 사용 중입니다.")
		a.finish("이미 최신 버전을 사용 중입니다.", true, false)
		return
	}
	a.status(fmt.Sprintf(">> 새 버전(%s)이 있습니다.", target.ver))

	// 업데이트를 제안하기 전에 targetDir가 TTD 설치 폴더가 맞는지(또는 비어 있는지) 확인한다 —
	// 엉뚱한 폴더(소스 폴더, 다운로드 폴더 등)에서 실행됐다면 업데이트 버튼 자체를 내놓지 않는다.
	if safe, reason := isSafeInstallDir(targetDir); !safe {
		a.status(fmt.Sprintf("[중단] %s", reason))
		a.finish(reason, false, true)
		return
	}

	// 자동으로 받지 않는다 — 여기서 멈추고 사용자가 "업데이트"를 눌러(StartUpdate) 확정할 때까지
	// 기다린다. 체크를 풀고 닫으면 이번엔 업데이트 없이 그대로 실행/종료한다.
	saveVer := target.ver
	a.updateMu.Lock()
	a.pending = &pendingUpdate{target: target, saveVer: saveVer}
	a.updateMu.Unlock()
	a.status(fmt.Sprintf(">> v%s 업데이트를 설치할 수 있습니다. 아래 \"업데이트\" 버튼을 눌러 주세요.", saveVer))
	// 버튼을 누르기 전에 미리 알려 준다(실제 차단은 applyUpdate의 ensureTTDNotRunning이 한다).
	if name := runningAppExe(targetDir, selfExeName()); name != "" {
		a.status(fmt.Sprintf("[안내] TTD(%s)가 실행 중입니다. 업데이트하려면 먼저 TTD를 종료해 주세요.", name))
	}
	a.finishWithUpdate(saveVer)
}

// ensureTTDNotRunning은 TTD가 실행 중이면(TTD.exe가 잠겨 덮어쓸 수 없으면) 업데이트를 대기
// 상태로 되돌리고 안내를 띄운 뒤 false를 돌려준다 — 사용자는 TTD를 끄고 같은 "업데이트" 버튼을
// 다시 누르면 된다. 파일을 하나라도 바꾸기 전에 확인해야 일부 파일만 새 버전으로 바뀐 채
// 멈추는 일이 없다. extractDir를 주면(압축 해제 후) zip이 TTD.exe를 설치할 하위 폴더까지 본다.
func (a *App) ensureTTDNotRunning(p pendingUpdate, extractDir string) bool {
	var name string
	if extractDir == "" {
		name = runningAppExe(targetDir, selfExeName())
	} else {
		name = runningAppExeForSync(extractDir, targetDir, selfExeName())
	}
	if name == "" {
		return true
	}
	a.blockUpdate(p, fmt.Sprintf("TTD(%s)가 실행 중이라 업데이트할 수 없습니다. TTD를 종료한 뒤 다시 \"업데이트\" 버튼을 눌러 주세요.", name))
	return false
}

// blockUpdate는 지금은 업데이트를 진행할 수 없어 대기 상태로 되돌린다 — "업데이트" 버튼이 다시
// 살아나고 프론트엔드가 msg를 대화상자로 띄우므로, 원인이 풀리면 창을 다시 열지 않고 재시도할 수 있다.
func (a *App) blockUpdate(p pendingUpdate, msg string) {
	a.status("[중단] " + msg)
	a.updateMu.Lock()
	a.pending = &p
	a.updateMu.Unlock()
	a.emitReady(ReadyPayload{Message: msg, UpdateAvailable: true, UpdateVersion: p.saveVer, Blocked: true})
}

// failDownload는 드라이브에서 zip을 받지 못했을 때 대기 상태로 되돌린다. blockUpdate와 같지만
// 사용자가 브라우저로 직접 받아 적용할 수 있게 그 zip의 드라이브 페이지 주소를 함께 보낸다.
func (a *App) failDownload(p pendingUpdate, msg string) {
	a.status("[중단] " + msg)
	p.tempDir = ""
	a.updateMu.Lock()
	a.pending = &p
	a.updateMu.Unlock()
	a.emitReady(ReadyPayload{
		Message:         msg,
		Error:           true,
		UpdateAvailable: true,
		UpdateVersion:   p.saveVer,
		Blocked:         true,
		DownloadFailed:  true,
		ManualURL:       p.target.manualURL(),
	})
}

// ApplyLocalZip은 "받은 zip으로 업데이트" 버튼에서 호출된다. 드라이브 다운로드가 막혔을 때
// 사용자가 브라우저로 받은 zip을 골라 대기 중인 업데이트에 그대로 쓴다 — 받아 둔 zip을 다시
// 쓰는 경로(pendingUpdate.tempDir)를 그대로 타므로, 이후 TTD 실행 중 확인·백업·적용은 평소와 같다.
// 시작했으면 true, 사용자가 취소했으면 false, 쓸 수 없는 파일이면 그 이유를 오류로 돌려준다.
func (a *App) ApplyLocalZip() (bool, error) {
	a.updateMu.Lock()
	pending := a.pending
	a.updateMu.Unlock()
	if pending == nil {
		return false, nil
	}

	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "받은 TTD 압축 파일 선택",
		Filters: []runtime.FileFilter{{DisplayName: "TTD 압축 파일 (*.zip)", Pattern: "*.zip"}},
	})
	if err != nil || path == "" {
		return false, nil
	}
	if err := checkTTDZip(path); err != nil {
		return false, err
	}

	ver := versionFromName(filepath.Base(path))
	if ver != pending.saveVer {
		what := fmt.Sprintf("선택한 파일의 버전(v%s)", ver)
		if ver == "0.0.0" {
			what = "선택한 파일 이름에서 버전을 알 수 없어"
		}
		answer, _ := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.QuestionDialog,
			Title:   "버전 확인",
			Message: fmt.Sprintf("%s 설치하려던 v%s와 다릅니다. 그래도 이 파일로 업데이트할까요?", what, pending.saveVer),
		})
		if answer != "Yes" {
			return false, nil
		}
	}

	a.updateMu.Lock()
	p := a.pending
	a.pending = nil
	a.updateMu.Unlock()
	if p == nil { // 그 사이 다른 경로로 업데이트가 시작됨
		return false, nil
	}
	tempDir, err := os.MkdirTemp("", "gdrive_update_")
	if err == nil {
		err = copyFile(path, filepath.Join(tempDir, p.target.name))
	}
	if err != nil {
		os.RemoveAll(tempDir)
		a.updateMu.Lock()
		a.pending = p
		a.updateMu.Unlock()
		return false, fmt.Errorf("선택한 파일을 복사하지 못했습니다: %w", err)
	}
	p.tempDir = tempDir
	if ver != "0.0.0" {
		p.saveVer = ver // 버전 기록은 실제로 적용하는 zip 기준
	}
	a.status(fmt.Sprintf("선택한 파일로 업데이트합니다. (%s)", path))
	go a.applyUpdate(*p)
	return true, nil
}

// pendingUpdate는 확인은 끝났지만 아직 사용자가 설치를 확정하지 않은 업데이트다.
type pendingUpdate struct {
	target  zipCandidate
	saveVer string // 적용 후 version_info.json에 기록할 버전
	// tempDir는 이미 zip을 받아 둔 임시 폴더다(다운로드 후 TTD 실행 중이라 막혔을 때만 설정).
	// 다시 누르면 zip을 또 받지 않고 이걸 쓴다. 그대로 창을 닫으면 다음 실행 때
	// cleanupStaleTempDirs가 지운다.
	tempDir string
}

// StartUpdate는 "업데이트" 버튼에서 호출된다. 대기 중인 업데이트를 백그라운드에서 받아 적용하고,
// 끝나면 평소처럼 "ready"를 보낸다. 중복 클릭이나 대기 중인 업데이트가 없을 때는 아무것도 안 한다.
func (a *App) StartUpdate() {
	a.updateMu.Lock()
	p := a.pending
	a.pending = nil
	a.updateMu.Unlock()
	if p != nil {
		go a.applyUpdate(*p)
	}
}

// applyUpdate는 대기 중이던 업데이트의 다운로드 → 압축 해제 → 파일 반영을 수행한다.
func (a *App) applyUpdate(p pendingUpdate) {
	if !a.ensureTTDNotRunning(p, "") {
		return
	}
	target := p.target

	tempDir := p.tempDir
	zipPath := filepath.Join(tempDir, target.name)
	if _, statErr := os.Stat(zipPath); tempDir == "" || statErr != nil {
		var err error
		tempDir, err = os.MkdirTemp("", "gdrive_update_")
		if err != nil {
			a.status(fmt.Sprintf("[오류 발생] %v", err))
			a.finish("업데이트 준비 중 오류가 발생했습니다.", false, true)
			return
		}
		zipPath = filepath.Join(tempDir, target.name)
		a.status(fmt.Sprintf("파일 다운로드 중... (%s)", target.name))
		progress := func(written, total int64) {
			if total > 0 {
				a.status(fmt.Sprintf("다운로드 중... %d%% (%s)", written*100/total, formatBytes(written, total)))
			} else {
				a.status(fmt.Sprintf("다운로드 중... (%s)", formatBytes(written, total)))
			}
		}
		err = target.download(zipPath, progress)
		if err != nil && target.id == "" {
			// 확인은 GitHub에서 됐어도 첨부 파일 서버만 막힌 네트워크가 있다 — 같은 버전이 드라이브에도
			// 있으면 그쪽으로 한 번 더 받아 본다.
			a.status(fmt.Sprintf("[알림] GitHub에서 받지 못했습니다 — 구글 드라이브에서 다시 시도합니다. (%v)", err))
			if dz, derr := latestDriveZip(newHTTPClient(20 * time.Second)); derr != nil {
				err = fmt.Errorf("%w (드라이브 확인도 실패: %v)", err, derr)
			} else if dz.ver != target.ver {
				err = fmt.Errorf("%w (드라이브에는 v%s가 없어 다시 시도하지 못했습니다)", err, target.ver)
			} else if derr = dz.download(zipPath, progress); derr != nil {
				err = fmt.Errorf("GitHub: %v / 드라이브: %w", err, derr)
			} else {
				err = nil
			}
		}
		if err != nil {
			os.RemoveAll(tempDir)
			const manualHint = "잠시 후 다시 \"업데이트\" 버튼을 누르거나, \"업데이트 수동으로 받기\"로 " +
				"브라우저에서 받은 뒤 \"받은 zip으로 업데이트\"를 눌러 주세요."
			if errors.Is(err, errDriveQuota) {
				a.failDownload(p, "구글 드라이브의 다운로드 한도를 초과해 지금은 TTD를 받을 수 없습니다. "+
					"새 버전이 나온 직후 사용자가 몰리면 생기며, 최대 24시간 걸릴 수 있습니다. "+manualHint)
				return
			}
			a.failDownload(p, fmt.Sprintf("업데이트 다운로드 실패: %v. %s", err, manualHint))
			return
		}
	} else {
		a.status(fmt.Sprintf("이미 받은 파일을 사용합니다. (%s)", target.name))
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			os.RemoveAll(tempDir)
		}
	}()

	// 3. 압축 해제 및 파일 반영
	a.status("압축 해제 및 파일 적용 중...")
	extractDir := filepath.Join(tempDir, "extracted")
	os.RemoveAll(extractDir) // 받아 둔 zip을 다시 쓰는 경우 이전 압축 해제 결과를 비운다
	if err := unzip(zipPath, extractDir); err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 적용 중 오류가 발생했습니다.", false, true)
		return
	}

	// 다운로드하는 동안 TTD를 켰을 수도 있으니 파일을 바꾸기 직전에 한 번 더 확인한다.
	// 막히면 받은 zip은 남겨 두고 다시 누를 때 그대로 쓴다.
	p.tempDir = tempDir
	if !a.ensureTTDNotRunning(p, extractDir) {
		keepTemp = true
		return
	}
	if err := syncExtractedFiles(extractDir, targetDir, selfExeName(), a.status); err != nil {
		a.status(fmt.Sprintf("[오류 발생] %v", err))
		a.finish("업데이트 적용 중 오류가 발생했습니다.", false, true)
		return
	}

	if p.saveVer != "0.0.0" {
		_ = saveLocalVersion(p.saveVer)
	}

	a.status("[성공] 업데이트가 완료되었습니다!")
	a.finish("업데이트가 완료되었습니다.", false, false)
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
	a.updatePrefs(func(p *Prefs) {
		p.GameLaunchMethod = method
		p.GameSteamAppID = steamAppID
		p.GameClientExePath = clientExePath
	})
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
	p := a.updatePrefs(func(p *Prefs) {
		p.LaunchOnClose = launchOnClose
		p.LaunchTTDAfterUpdate = launchTTDNow
		p.LaunchGameAfterUpdate = launchGameNow
		p.CreateDesktopShortcut = createShortcutNow
	})

	// 실패를 조용히 삼키면 사용자는 "완료를 눌러도 아무 반응이 없다"고만 느끼게 되므로,
	// 창을 닫기 전에 무엇이 왜 실패했는지 대화상자로 보여준다.
	var problems []string
	if launchOnClose {
		if launchTTDNow {
			if err := launchTTD(); err != nil {
				problems = append(problems, fmt.Sprintf("TTD 실행 실패: %v", err))
			}
		}
		if launchGameNow && p.GameLaunchMethod != "" {
			launchGame(p)
		}
	}

	if createShortcutNow {
		if err := createDesktopShortcut(); err != nil {
			problems = append(problems, fmt.Sprintf("바탕화면 바로가기 생성 실패: %v", err))
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			a.status("[오류 발생] " + p)
		}
		_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "ttd_updater",
			Message: strings.Join(problems, "\n\n"),
		})
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

// SetTheme은 화면 테마 선택("light"/"dark", ""=시스템 설정)을 바로 저장한다 — 다른 옵션처럼
// "완료" 때 모아 저장하지 않는 건, 테마는 누르는 즉시 화면에 반영되는 설정이라서다.
func (a *App) SetTheme(theme string) {
	a.updatePrefs(func(p *Prefs) { p.Theme = theme })
}

// SetSkipUpdate는 "TTD vX 업데이트 설치" 체크를 바꿀 때마다 바로 저장한다 — 완료 버튼이 아니라
// 창의 X로 닫아도 다음 실행에 해제 상태가 유지되게. 다시 체크하면 건너뛰기를 지운다.
func (a *App) SetSkipUpdate(version string, skip bool) {
	a.updatePrefs(func(p *Prefs) {
		if skip {
			p.SkipUpdateVersion = version
		} else if p.SkipUpdateVersion == version {
			p.SkipUpdateVersion = ""
		}
	})
}

// updatePrefs는 prefsMu를 잡은 채로 prefs를 고치고 저장한 뒤, 고친 결과의 사본을 돌려준다.
func (a *App) updatePrefs(change func(*Prefs)) Prefs {
	a.prefsMu.Lock()
	defer a.prefsMu.Unlock()
	change(&a.prefs)
	_ = savePrefs(a.prefs)
	return a.prefs
}

// DownloadPageURL은 TTD 배포 페이지(GitHub Releases) 주소다 — 프론트엔드의 "TTD 다운로드" 바로가기가
// 업데이트 소스와 어긋나지 않게 같은 상수를 그대로 쓴다.
func (a *App) DownloadPageURL() string {
	return ttdReleasesPage
}

// UpdaterVersion은 이 런처(ttd_updater) 빌드 자신의 버전이다 — 헤더 "업데이트 · 런처 vX" 표시용.
func (a *App) UpdaterVersion() string {
	return updaterVersion
}
