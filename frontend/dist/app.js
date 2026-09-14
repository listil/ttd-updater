const logEl = document.getElementById("log");
const headerText = document.getElementById("headerText");
const statusDot = document.getElementById("statusDot");

const launchOnClose = document.getElementById("launchOnClose");
const launchTargets = document.getElementById("launchTargets");
const launchTTD = document.getElementById("launchTTD");
const launchGame = document.getElementById("launchGame");
const gameSetupBtn = document.getElementById("gameSetupBtn");
const gameSetupPanel = document.getElementById("gameSetupPanel");
const methodSteam = document.getElementById("methodSteam");
const methodClient = document.getElementById("methodClient");
const steamAppId = document.getElementById("steamAppId");
const clientExePath = document.getElementById("clientExePath");
const browseBtn = document.getElementById("browseBtn");
const gameSetupSave = document.getElementById("gameSetupSave");
const confirmBtn = document.getElementById("confirmBtn");
const gameMethodSummary = document.getElementById("gameMethodSummary");
const autoCloseHint = document.getElementById("autoCloseHint");
const waitingHint = document.getElementById("waitingHint");
const createShortcut = document.getElementById("createShortcut");
const selfUpdateSection = document.getElementById("selfUpdateSection");
const installSelfUpdate = document.getElementById("installSelfUpdate");
const selfUpdateLabel = document.getElementById("selfUpdateLabel");

let savedMethod = null;
let autoCloseTimer = null;
let autoCloseRemaining = 0;

// main.go의 초기 Width/Height와 맞춘 기본 창 크기. 실행 방식 설정 패널처럼 내용이 늘어나면
// 창 안에서 스크롤바가 생기는 대신 창 자체를 실제 내용 높이에 맞춰 키운다/줄인다.
const BASE_WINDOW_WIDTH = 440;
const BASE_WINDOW_HEIGHT = 480;

const appEl = document.querySelector(".app");

function resizeWindowToContent() {
  requestAnimationFrame(() => {
    // .app은 CSS에서 height:100vh로 고정돼 있어서(내부는 flex로 넘치는 부분을 로그
    // 스크롤로 흡수) scrollHeight를 그대로 재면 항상 현재 창 높이가 나온다. 실제
    // 필요한 높이를 재려면 잠깐 고정을 풀고 자연스러운 높이로 펼쳐본 뒤 되돌린다.
    const prevHeight = appEl.style.height;
    appEl.style.height = "auto";
    const needed = appEl.scrollHeight;
    appEl.style.height = prevHeight;

    const height = Math.max(BASE_WINDOW_HEIGHT, needed + 4);
    window.runtime.WindowSetSize(BASE_WINDOW_WIDTH, height);
  });
}

// "이미 최신 버전"일 때만 몇 초 후 자동으로 완료 처리한다. 체크박스를 만지거나 버튼에
// 마우스를 올리면(사용자가 뭔가 조작하려는 낌새) 바로 취소한다.
function startAutoClose(seconds) {
  autoCloseRemaining = seconds;
  autoCloseHint.hidden = false;
  autoCloseHint.textContent = `${autoCloseRemaining}초 후 자동 종료...`;
  autoCloseTimer = setInterval(() => {
    autoCloseRemaining -= 1;
    if (autoCloseRemaining <= 0) {
      cancelAutoClose();
      window.go.main.App.Confirm(
        launchOnClose.checked,
        launchTTD.checked,
        launchGame.checked,
        createShortcut.checked,
        installSelfUpdate.checked
      );
      return;
    }
    autoCloseHint.textContent = `${autoCloseRemaining}초 후 자동 종료...`;
  }, 1000);
}

function cancelAutoClose() {
  if (autoCloseTimer) {
    clearInterval(autoCloseTimer);
    autoCloseTimer = null;
  }
  autoCloseHint.hidden = true;
}

launchTTD.addEventListener("change", cancelAutoClose);
launchGame.addEventListener("change", cancelAutoClose);
launchOnClose.addEventListener("change", cancelAutoClose);
createShortcut.addEventListener("change", cancelAutoClose);
installSelfUpdate.addEventListener("change", cancelAutoClose);
confirmBtn.addEventListener("mouseenter", cancelAutoClose);
gameSetupBtn.addEventListener("mouseenter", cancelAutoClose);

// launchOnClose는 "종료 시 실제로 실행할지"이고 launchTTD/launchGame은 "그중 무엇을
// 대상으로 할지"이므로, 전자가 꺼져 있으면 대상 선택 UI를 비활성화해 혼동을 막는다.
function refreshLaunchTargetsEnabled() {
  launchTargets.classList.toggle("disabled", !launchOnClose.checked);
}
launchOnClose.addEventListener("change", refreshLaunchTargetsEnabled);

function refreshMethodSummary() {
  if (savedMethod === "steam") {
    gameMethodSummary.textContent = `Steam으로 실행 (App ID: ${steamAppId.value || DEFAULT_STEAM_APP_ID})`;
  } else if (savedMethod === "client") {
    gameMethodSummary.textContent = `공식 클라이언트로 실행 (${clientExePath.value || "경로 미설정"})`;
  } else {
    gameMethodSummary.textContent = "실행 방식이 설정되지 않았습니다.";
  }
}

// 토치라이트: 인피니트의 Steam App ID. 게임마다 Valve가 부여하는 고정값이라
// 사용자/PC에 따라 달라지지 않으므로 기본값으로 미리 채워둔다.
const DEFAULT_STEAM_APP_ID = "1974050";

function appendLine(text, cls) {
  const line = document.createElement("div");
  line.className = "line" + (cls ? " " + cls : "");
  line.textContent = text;
  logEl.appendChild(line);
  logEl.scrollTop = logEl.scrollHeight;
}

window.runtime.EventsOn("status", (text) => {
  const isHighlight = text.startsWith(">>") || text.includes("성공") || text.includes("오류");
  appendLine(text, isHighlight ? "highlight" : "muted");
  headerText.textContent = text.replace(/^>>\s*/, "").replace(/^\[.*?\]\s*/, "");
});

// "init"(업데이트 확인이 시작되기도 전, 창이 뜨자마자)과 "ready"(업데이트 확인/적용이 끝난 뒤)
// 둘 다 같은 모양의 체크박스 상태를 담고 있어서 반영 로직을 공유한다 — 옵션 자체는 진행 상황과
// 무관하게 항상 저장된 값을 그대로 보여주면 되기 때문이다.
function applyPrefsPayload(payload) {
  launchOnClose.checked = payload.launchOnClose;
  launchTTD.checked = payload.launchTTD;
  launchGame.checked = payload.launchGame;
  createShortcut.checked = payload.createDesktopShortcut;
  refreshLaunchTargetsEnabled();

  savedMethod = payload.gameLaunchMethod || null;
  methodSteam.checked = savedMethod !== "client";
  methodClient.checked = savedMethod === "client";
  steamAppId.value = payload.gameSteamAppID || DEFAULT_STEAM_APP_ID;
  clientExePath.value = payload.gameClientExePath || "";
  refreshMethodSummary();
}

// 창이 뜨자마자(업데이트 확인이 끝나길 기다리지 않고) 옵션들을 미리 보여준다. "완료" 버튼은
// 아직 비활성 상태 — 실제로 닫아도 되는 시점(ready)이 되면 활성화된다.
window.runtime.EventsOn("init", (payload) => {
  applyPrefsPayload(payload);
  resizeWindowToContent();
});

window.runtime.EventsOn("ready", (payload) => {
  const isError = payload.message.includes("오류");
  statusDot.className = "dot " + (isError ? "error" : "done");
  headerText.textContent = payload.message;

  applyPrefsPayload(payload);
  confirmBtn.disabled = false;
  waitingHint.hidden = true;

  if (payload.selfUpdateAvailable) {
    selfUpdateSection.hidden = false;
    selfUpdateLabel.textContent = `ttd_updater 새 버전(v${payload.selfUpdateVersion}) 설치`;
  } else {
    selfUpdateSection.hidden = true;
    installSelfUpdate.checked = false;
  }

  resizeWindowToContent();

  if (payload.alreadyLatest) {
    startAutoClose(5);
  }
});

gameSetupBtn.addEventListener("click", async () => {
  gameSetupPanel.hidden = !gameSetupPanel.hidden;
  resizeWindowToContent();

  // 아직 경로를 고른 적 없으면, 통상적인 설치 경로에 실제로 파일이 있는지 확인해서
  // 있을 때만 "찾아보기 전 제안값"으로 미리 채워준다(없으면 빈 값 그대로 둠).
  if (!gameSetupPanel.hidden && !clientExePath.value.trim()) {
    const defaultPath = await window.go.main.App.DefaultClientExePath();
    if (defaultPath) {
      clientExePath.value = defaultPath;
    }
  }
});

browseBtn.addEventListener("click", async () => {
  const path = await window.go.main.App.BrowseForGameExe();
  if (path) {
    clientExePath.value = path;
  }
});

gameSetupSave.addEventListener("click", async () => {
  const method = methodClient.checked ? "client" : "steam";
  if (method === "steam" && !steamAppId.value.trim()) {
    alert("Steam App ID를 입력해 주세요.");
    return;
  }
  if (method === "client" && !clientExePath.value.trim()) {
    alert("실행 파일 경로를 선택해 주세요.");
    return;
  }
  await window.go.main.App.SaveGameSetup(method, steamAppId.value.trim(), clientExePath.value.trim());
  savedMethod = method;
  refreshMethodSummary();
  gameSetupPanel.hidden = true;
  resizeWindowToContent();
});

confirmBtn.addEventListener("click", async () => {
  cancelAutoClose();
  if (launchOnClose.checked && launchGame.checked && !savedMethod) {
    gameSetupPanel.hidden = false;
    resizeWindowToContent();
    alert("먼저 토치라이트 실행 방식을 설정해 주세요.");
    return;
  }
  await window.go.main.App.Confirm(
    launchOnClose.checked,
    launchTTD.checked,
    launchGame.checked,
    createShortcut.checked,
    installSelfUpdate.checked
  );
});
