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
const notesListEl = document.getElementById("notesList");
const launcherNoteEl = document.getElementById("launcherNote");
const selfNotesBtn = document.getElementById("selfNotesBtn");
const localVerEl = document.getElementById("localVer");
const remoteVerEl = document.getElementById("remoteVer");
const progressBar = document.getElementById("progressBar");
const logDetails = document.getElementById("logDetails");

let savedMethod = null;
let autoCloseTimer = null;
let autoCloseRemaining = 0;
let remoteVersion = "";
// "init"으로 저장된 설정을 이미 화면에 반영했는지. 반영한 뒤로는 체크박스가 사용자 것이므로
// "ready"가 다시 덮어쓰지 않는다(다운로드 중에 바꾼 선택이 되돌아가지 않게).
let prefsApplied = false;

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

// ttd_updater 자신의 새 버전 릴리즈 노트 — TTD 노트 목록과 따로 맨 위 카드에 그린다
// ("notes"가 목록을 다시 그려도 지워지지 않게 #notesList 밖에 둔다). 카드는 숨겨 두고,
// 자체 업데이트 체크박스 옆 "변경 내용" 링크로 여닫는다 — 업데이트 내역은 TTD 노트가 주인공이다.
function renderLauncherNote(s) {
  const head = document.createElement("div");
  head.className = "note-head";
  const tag = document.createElement("span");
  tag.className = "note-tag";
  tag.textContent = "런처";
  const ver = document.createElement("span");
  ver.className = "note-ver";
  ver.textContent = "ttd_updater v" + s.version;
  const badge = document.createElement("span");
  badge.className = "note-new";
  badge.textContent = "NEW";
  const date = document.createElement("span");
  date.className = "note-date";
  date.textContent = s.date || "";
  head.append(tag, ver, badge, date);

  const body = document.createElement("div");
  body.className = "note-body";
  for (const line of s.lines || []) body.appendChild(renderNoteLine(line));

  launcherNoteEl.replaceChildren(head, body);
  selfNotesBtn.hidden = false;
}

function renderNotes(payload) {
  notesListEl.replaceChildren();
  const sections = payload.sections || [];
  if (sections.length === 0) {
    const empty = document.createElement("div");
    empty.className = "notes-empty";
    empty.textContent = "업데이트 내역을 불러오지 못했습니다.";
    notesListEl.appendChild(empty);
    return;
  }

  // NEW 여부는 백엔드(markNewSections)가 계산한다. 같은 버전이 노트에 두 번 적혀 있을 수
  // 있으므로(예: v3.4.5 두 섹션) 개수는 섹션이 아니라 버전 기준으로 센다.
  const newVersions = new Set();

  for (const s of sections) {
    const isNew = s.isNew;
    if (isNew) newVersions.add(s.version);

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
    notesListEl.appendChild(card);
  }

  notesBadge.hidden = newVersions.size === 0;
  notesBadge.textContent = `새 버전 ${newVersions.size}개`;
}

// 확인은 끝났고 사용자의 설치 확정을 기다리는 업데이트 버전("" = 없음). 이 동안 아래 버튼은
// "설치 체크박스"가 켜져 있으면 "업데이트" 버튼이 되고, 누르면 그때 다운로드를 시작한다.
let pendingUpdateVersion = "";
const updateSection = document.getElementById("updateSection");
const installUpdate = document.getElementById("installUpdate");
const updateLabel = document.getElementById("updateLabel");

function updateWillRun() {
  return pendingUpdateVersion !== "" && installUpdate.checked;
}

// 실행 버튼 문구: 업데이트 대기 중이면 "업데이트", 아니면 실제로 무언가 실행될 때만 "실행".
function refreshLaunchLabel() {
  if (updateWillRun()) {
    confirmBtn.textContent = `⬇  TTD v${pendingUpdateVersion} 업데이트`;
    return;
  }
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
// 업데이트 내역을 읽는 중(스크롤/클릭/키 입력)에도 창이 닫히지 않게 한다.
for (const type of ["pointerdown", "wheel", "keydown"]) {
  document.addEventListener(type, cancelAutoClose, { passive: true });
}
notesEl.addEventListener("scroll", cancelAutoClose, { passive: true });

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
window.runtime.EventsOn("selfNotes", renderLauncherNote);

// "변경 내용" → 업데이트 내역 맨 위에 런처 카드를 열고(맨 위로 스크롤), 다시 누르면 닫는다.
selfNotesBtn.addEventListener("click", () => {
  cancelAutoClose();
  const open = launcherNoteEl.hidden;
  launcherNoteEl.hidden = !open;
  selfNotesBtn.textContent = open ? "닫기" : "변경 내용";
  if (open) notesEl.scrollTo({ top: 0, behavior: "smooth" });
});

window.runtime.EventsOn("versions", (v) => {
  localVerEl.textContent = v.local && v.local !== "0.0.0" ? "v" + v.local : "없음";
  if (v.remote) {
    remoteVersion = v.remote;
    remoteVerEl.textContent = "v" + v.remote;
  }
});

// "init"(업데이트 확인이 시작되기도 전, 창이 뜨자마자)과 "ready"(업데이트 확인/적용이 끝난 뒤)
// 둘 다 같은 모양의 체크박스 상태를 담고 있어서 반영 로직을 공유한다. 단 실제로 반영하는 건
// 처음 한 번뿐이다 — "ready"는 "init"이 (프론트엔드가 뜨기 전에 보내져) 유실된 경우의 대비책.
function applyPrefsPayload(payload) {
  if (prefsApplied) return;
  prefsApplied = true;
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
  const isError = payload.error;
  const state = isError ? "error" : payload.updateAvailable ? "pending" : "done";
  statusDot.className = "dot " + state;
  progressBar.className = "progress-bar" + (state === "done" ? "" : " " + state);
  headerText.textContent = payload.message;
  headerText.title = payload.message;
  if (isError) logDetails.open = true;

  // 업데이트가 실제로 적용됐다면 이제 현재 버전 = 최신 버전
  if (state === "done" && !payload.alreadyLatest && remoteVersion) {
    localVerEl.textContent = "v" + remoteVersion;
  }

  // "ready"는 업데이트 대기(updateAvailable) → 업데이트 완료 순서로 두 번 올 수 있다.
  pendingUpdateVersion = payload.updateAvailable ? payload.updateVersion : "";
  updateSection.hidden = !payload.updateAvailable;
  if (payload.updateAvailable) {
    // 지난번에 이 버전의 설치 체크를 해제했다면 그대로 해제 상태로 보여준다(새 버전이면 다시 체크).
    installUpdate.checked = payload.skipUpdateVersion !== payload.updateVersion;
    updateLabel.textContent = `TTD v${payload.updateVersion} 업데이트 설치`;
  }

  applyPrefsPayload(payload);
  refreshLaunchLabel();
  confirmBtn.disabled = false;
  waitingHint.hidden = true;

  if (payload.selfUpdateAvailable) {
    selfUpdateSection.hidden = false;
    selfUpdateLabel.textContent = `종료 시 런처 v${payload.selfUpdateVersion} 설치`;
  } else {
    selfUpdateSection.hidden = true;
    installSelfUpdate.checked = false;
  }

  if (payload.alreadyLatest) {
    startAutoClose(5);
  }

  // TTD 실행 중이라 업데이트가 막힌 경우 — 헤더 문구만으로는 놓치기 쉬워 대화상자로도 알린다.
  // 업데이트 버튼은 위에서 다시 살아나 있으므로 TTD를 끄고 그대로 다시 누르면 된다.
  if (payload.blocked) {
    alert(payload.message);
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

installUpdate.addEventListener("change", () => {
  refreshLaunchLabel();
  if (pendingUpdateVersion) {
    window.go.main.App.SetSkipUpdate(pendingUpdateVersion, !installUpdate.checked);
  }
});

confirmBtn.addEventListener("click", async () => {
  cancelAutoClose();

  // 업데이트 대기 중 + 설치 체크 → 창을 닫지 않고 지금 다운로드/적용을 시작한다. 끝나면 백엔드가
  // 다시 "ready"를 보내 이 버튼이 평소의 실행/닫기 버튼으로 돌아온다.
  if (updateWillRun()) {
    const version = pendingUpdateVersion;
    pendingUpdateVersion = "";
    updateSection.hidden = true;
    confirmBtn.disabled = true;
    confirmBtn.textContent = `v${version} 업데이트 중...`;
    statusDot.className = "dot";
    progressBar.className = "progress-bar working";
    headerText.textContent = "업데이트를 내려받는 중...";
    await window.go.main.App.StartUpdate();
    return;
  }

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

// 바로가기: WebView 안에서 페이지를 여는 대신 사용자의 기본 브라우저로 연다. data-url이 없는
// "TTD 다운로드"는 업데이트 소스와 어긋나지 않도록 백엔드의 folderURL을 그대로 받아 쓴다.
document.querySelectorAll(".link-chip").forEach((b) =>
  b.addEventListener("click", async () => {
    const url = b.dataset.url || (await window.go.main.App.FolderURL());
    window.runtime.BrowserOpenURL(url);
  })
);

// 헤더 "업데이트 · 런처" 옆에 런처 자신의 현재 버전을 표시한다.
window.go.main.App.UpdaterVersion().then((v) => {
  document.getElementById("updaterVer").textContent = "v" + v;
});
