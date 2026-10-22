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

const notesEl = document.getElementById("notes");
const notesBadge = document.getElementById("notesBadge");
const localVerEl = document.getElementById("localVer");
const remoteVerEl = document.getElementById("remoteVer");
const progressBar = document.getElementById("progressBar");
const logDetails = document.getElementById("logDetails");

let savedMethod = null;
let autoCloseTimer = null;
let autoCloseRemaining = 0;
let remoteVersion = "";

// "1.2.3" 형식끼리 비교: a가 b보다 크면 양수.
function compareVersions(a, b) {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0);
  }
  return 0;
}

// 노트 원문의 줄 머리표("*" 소제목, "-" 항목)를 해석해 섹션 본문을 만든다.
function renderNoteLine(text) {
  const el = document.createElement("div");
  const trimmed = text.trim();
  if (trimmed === "") {
    el.className = "note-gap";
  } else if (trimmed.startsWith("*")) {
    el.className = "note-sub";
    el.textContent = trimmed.replace(/^\*\s*/, "");
  } else if (trimmed.startsWith("-")) {
    el.className = "note-item";
    el.textContent = trimmed.replace(/^-\s*/, "");
  } else {
    el.className = "note-text";
    el.textContent = trimmed;
  }
  return el;
}

function renderNotes(payload) {
  notesEl.replaceChildren();
  const sections = payload.sections || [];
  if (sections.length === 0) {
    const empty = document.createElement("div");
    empty.className = "notes-empty";
    empty.textContent = "업데이트 내역을 불러오지 못했습니다.";
    notesEl.appendChild(empty);
    return;
  }

  // 버전 정보가 없는 새 설치(0.0.0)는 전부 "새 버전"이 되므로 NEW 표시를 하지 않는다.
  const local = payload.localVersion;
  const markNew = local && local !== "0.0.0";
  let newCount = 0;

  for (const s of sections) {
    const isNew = markNew && compareVersions(s.version, local) > 0;
    if (isNew) newCount++;

    const card = document.createElement("article");
    card.className = "note" + (isNew ? " is-new" : "");

    const head = document.createElement("div");
    head.className = "note-head";
    const ver = document.createElement("span");
    ver.className = "note-ver";
    ver.textContent = "v" + s.version;
    head.appendChild(ver);
    if (isNew) {
      const badge = document.createElement("span");
      badge.className = "note-new";
      badge.textContent = "NEW";
      head.appendChild(badge);
    }
    const date = document.createElement("span");
    date.className = "note-date";
    date.textContent = s.date || "";
    head.appendChild(date);

    const body = document.createElement("div");
    body.className = "note-body";
    for (const line of s.lines || []) body.appendChild(renderNoteLine(line));

    card.append(head, body);
    notesEl.appendChild(card);
  }

  notesBadge.hidden = newCount === 0;
  notesBadge.textContent = `새 버전 ${newCount}개`;
}

// 실행 버튼 문구: 실제로 무언가 실행될 때만 "실행"이라고 쓴다.
function refreshLaunchLabel() {
  const targets = [];
  if (launchTTD.checked) targets.push("TTD");
  if (launchGame.checked) targets.push("토치라이트");
  confirmBtn.textContent =
    launchOnClose.checked && targets.length > 0 ? `▶  ${targets.join(" + ")} 실행` : "닫기";
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
launchOnClose.addEventListener("change", refreshLaunchLabel);
launchTTD.addEventListener("change", refreshLaunchLabel);
launchGame.addEventListener("change", refreshLaunchLabel);

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

// 업데이트 확인과 병렬로 받아온 업데이트 내역(최신 섹션들) — 왼쪽 패널에 그린다.
window.runtime.EventsOn("notes", renderNotes);

window.runtime.EventsOn("versions", (v) => {
  localVerEl.textContent = v.local && v.local !== "0.0.0" ? "v" + v.local : "없음";
  if (v.remote) {
    remoteVersion = v.remote;
    remoteVerEl.textContent = "v" + v.remote;
  }
});

// "init"(업데이트 확인이 시작되기도 전, 창이 뜨자마자)과 "ready"(업데이트 확인/적용이 끝난 뒤)
// 둘 다 같은 모양의 체크박스 상태를 담고 있어서 반영 로직을 공유한다 — 옵션 자체는 진행 상황과
// 무관하게 항상 저장된 값을 그대로 보여주면 되기 때문이다.
function applyPrefsPayload(payload) {
  applyTheme(payload.theme || "");
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
  refreshLaunchLabel();
}

// 창이 뜨자마자(업데이트 확인이 끝나길 기다리지 않고) 옵션들을 미리 보여준다. 실행 버튼은
// 아직 비활성 상태 — 실제로 닫아도 되는 시점(ready)이 되면 활성화된다.
window.runtime.EventsOn("init", applyPrefsPayload);

window.runtime.EventsOn("ready", (payload) => {
  // "오류" 외에 설치 폴더 안전장치로 중단된 경우("[중단]" 사유 메시지)도 실패로 표시한다.
  const isError = payload.message.includes("오류") || payload.message.includes("폴더");
  statusDot.className = "dot " + (isError ? "error" : "done");
  progressBar.className = "progress-bar" + (isError ? " error" : "");
  headerText.textContent = payload.message;
  headerText.title = payload.message;
  if (isError) logDetails.open = true;

  // 업데이트가 실제로 적용됐다면 이제 현재 버전 = 최신 버전
  if (!isError && !payload.alreadyLatest && remoteVersion) {
    localVerEl.textContent = "v" + remoteVersion;
  }

  applyPrefsPayload(payload);
  confirmBtn.disabled = false;
  waitingHint.hidden = true;

  if (payload.selfUpdateAvailable) {
    selfUpdateSection.hidden = false;
    selfUpdateLabel.textContent = `종료 시 ttd_updater 새 버전(v${payload.selfUpdateVersion}) 설치`;
  } else {
    selfUpdateSection.hidden = true;
    installSelfUpdate.checked = false;
  }


  if (payload.alreadyLatest) {
    startAutoClose(5);
  }
});

gameSetupBtn.addEventListener("click", async () => {
  gameSetupPanel.hidden = !gameSetupPanel.hidden;

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
});

confirmBtn.addEventListener("click", async () => {
  cancelAutoClose();
  if (launchOnClose.checked && launchGame.checked && !savedMethod) {
    gameSetupPanel.hidden = false;
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

// 테마: ""=시스템 설정, "light"/"dark"=고정. 저장은 백엔드 prefs(updater_prefs.json)가 하고,
// localStorage 사본은 다음 실행 때 "init" 이벤트가 오기 전 잠깐 반대 테마가 번쩍이는 것만 막는다.
const themeButtons = document.querySelectorAll("#themeSwitch button");

function applyTheme(theme) {
  if (theme) document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
  themeButtons.forEach((b) => b.classList.toggle("active", b.dataset.themeValue === (theme || "")));
  try {
    localStorage.setItem("theme", theme || "");
  } catch (e) {}
}

try {
  applyTheme(localStorage.getItem("theme") || "");
} catch (e) {
  applyTheme("");
}

themeButtons.forEach((b) =>
  b.addEventListener("click", () => {
    applyTheme(b.dataset.themeValue);
    window.go.main.App.SetTheme(b.dataset.themeValue);
  })
);

// 바로가기: WebView 안에서 페이지를 여는 대신 사용자의 기본 브라우저로 연다.
// (드라이브 주소는 app.go의 folderURL과 같은 폴더)
document.querySelectorAll(".link-chip").forEach((b) =>
  b.addEventListener("click", () => window.runtime.BrowserOpenURL(b.dataset.url))
);
