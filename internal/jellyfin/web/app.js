"use strict";
// Einfache Mediathek für die App „Jellyfin“ (iPhone/Android), die nur eine
// Weboberfläche anzeigt. Nutzt ausschließlich die Jellyfin-Schnittstelle von
// DS Video Revival (relative Pfade: läuft unter http://NAS:8096/web/ und
// https://NAS:5001/jellyfin/web/).
(function () {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const BASE = location.pathname.replace(/\/web(\/.*)?$/i, "");

  // ---------- Sprache (Gerät) ----------
  const de = ((navigator.languages && navigator.languages[0]) || navigator.language || "de").toLowerCase().startsWith("de");
  const EN = {
    "Suchen": "Search", "Abmelden": "Sign out", "Konto": "Account", "Passwort": "Password", "Anmelden": "Sign in",
    "Mit Quick Connect anmelden": "Sign in with Quick Connect", "Anderen Server wählen": "Choose another server",
    "Server wechseln: in der App unten auf „Einstellungen“ tippen und dort den Server wählen oder hinzufügen.": "To switch servers, tap “Settings” at the bottom of the app and choose or add the server there.",
    "Code in der Weboberfläche von DS Video Revival unter „Jellyfin-Apps“ bestätigen:": "Confirm this code in the DS Video Revival web interface under “Jellyfin apps”:",
    "Wartet auf Bestätigung …": "Waiting for confirmation …", "Schließen": "Close", "Startseite": "Home",
    "Weiterschauen": "Continue watching", "Als Nächstes": "Next up", "Neu: {0}": "New: {0}", "Keine Titel.": "No titles.",
    "A–Z": "A–Z", "Zuletzt hinzugefügt": "Recently added", "Erscheinungsjahr": "Release year", "Bewertung": "Rating",
    "Erneut versuchen": "Try again", "Suche: {0}": "Search: {0}", "▶ Abspielen": "▶ Play", "▶ Fortsetzen ab {0}": "▶ Resume from {0}",
    "Staffel {0}": "Season {0}", "Folge {0}": "Episode {0}", "{0} Min.": "{0} min", "Regie:": "Director:", "Darsteller:": "Cast:",
    "Wird vorbereitet …": "Preparing …", "Wiedergabe nicht möglich: {0}": "Playback not possible: {0}",
    "Direktwiedergabe fehlgeschlagen – wird umgewandelt …": "Direct play failed – transcoding …",
    "Dieses Gerät kann HLS nicht abspielen.": "This device cannot play HLS.", "Gesehen": "Watched",
    "Konto oder Passwort falsch": "Wrong account or password", "Server nicht erreichbar": "Server not reachable",
    "Menü": "Menu", "App-Einstellungen": "App settings",
    "Lade …": "Loading …", "Folgen": "Episodes", "Umgewandelt": "Transcoded", "Zurück": "Back",
    "Wiedergabe fehlgeschlagen – Ton wird umgewandelt …": "Playback failed – converting audio …",
    "Wiedergabe fehlgeschlagen – wird vollständig umgewandelt …": "Playback failed – fully transcoding …",
    "Vollständig umgewandelt": "Fully transcoded", "Zeitüberschreitung": "Timeout",
    "Apple-Player startet nicht – eigener Player …": "Apple player does not start – using built-in player …", "eigener Player": "built-in player",
    "Datei: {0}": "File: {0}", "Leitung ca. {0} Mbit/s": "Connection approx. {0} Mbit/s", "aufs iPhone": "to the iPhone", "aufs Handy": "to the phone", "fürs iPhone": "for the iPhone", "fürs Handy": "for the phone", "zum Herunterladen": "for download",
    "noch {0}": "{0} left", "gleich fertig": "almost done", "Heruntergeladen": "Downloaded",
    "Auf dem iPhone – unter „Downloads“ abspielbar": "On the iPhone – play it under “Downloads”",
    "Auf dem Handy – unter „Downloads“ abspielbar": "On the phone – play it under “Downloads”",
    "Übertragung stockt bei {0} % – App im Vordergrund lassen": "Transfer stalled at {0} % – keep the app in the foreground",
    "Wird {0} übertragen … {1} %": "Transferring {0} … {1} %", "Wird heruntergeladen … {0} %": "Downloading … {0} %",
    "Download nicht gestartet – bitte erneut versuchen.": "Download did not start – please try again.",
    "Download noch nicht gestartet – die App lädt voreingestellt nur im WLAN. Stand: in der Benachrichtigung bzw. unter „Downloads“.": "Download has not started yet – by default the app only downloads over Wi-Fi. Status: in the notification or under “Downloads”.",
    "Übertragung stockt bei {0} %": "Transfer stalled at {0} %", "Download wird gestartet …": "Starting download …",
    "Wartet auf die Umwandlung …": "Waiting for conversion …", "Wird {0} vorbereitet … {1} %": "Preparing {0} … {1} %",
    "Lädt …": "Loading …",
    "⤓ Herunterladen": "⤓ Download", "Downloads öffnen": "Open downloads", 
    
    "Umwandlung fehlgeschlagen: {0}": "Conversion failed: {0}", "Herunterladen nicht möglich: {0}": "Download not possible: {0}",
    "⤓ Originaldatei": "⤓ Original file", "Server: {0}": "Server: {0}", "nach {0} s": "after {0} s", "Prüfe Server …": "Checking server …",
  };
  const t = (k, ...a) => (de ? k : EN[k] || k).replace(/\{(\d)\}/g, (_, i) => a[+i] ?? "");
  document.documentElement.lang = de ? "de" : "en";
  $$("[data-t]").forEach((el) => (el.textContent = t(el.dataset.t)));
  $$("[data-ph]").forEach((el) => (el.placeholder = t(el.dataset.ph)));

  // ---------- Anmeldung ----------
  const KEY = "dsvr_jf" + BASE;
  const store = {
    get(k) { try { return JSON.parse(localStorage.getItem(KEY + k) || "null"); } catch (e) { return null; } },
    set(k, v) { try { v == null ? localStorage.removeItem(KEY + k) : localStorage.setItem(KEY + k, JSON.stringify(v)); } catch (e) { /* kein Speicher */ } },
  };
  let auth = store.get("auth"); // {token, user, name}
  let deviceId = store.get("dev");
  if (!deviceId) { deviceId = Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(16).padStart(2, "0")).join(""); store.set("dev", deviceId); }
  const ua = navigator.userAgent;
  const devName = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android" : "Browser";
  const header = () => `MediaBrowser Client="DS Video Revival Web", Device="${encodeURIComponent(devName)}", DeviceId="${deviceId}", Version="1.0"` + (auth ? `, Token="${auth.token}"` : "");

  async function api(path, opts = {}) {
    let r;
    try { r = await fetch(BASE + path, { ...opts, headers: { Authorization: header(), "Content-Type": "application/json", ...(opts.headers || {}) } }); }
    catch (e) { throw new Error(t("Server nicht erreichbar")); }
    if (r.status === 401 && auth && !path.startsWith("/Users/Authenticate")) { signedOut(); throw new Error("401"); }
    if (r.status === 204) return null;
    const text = await r.text();
    if (!r.ok) throw new Error(text || String(r.status));
    try { return JSON.parse(text); } catch (e) { return text; }
  }

  function signedIn(res) {
    auth = { token: res.AccessToken, user: res.User.Id, name: res.User.Name };
    store.set("auth", auth);
    stopQC();
    try { history.replaceState({ d: 0 }, "", "#/"); } catch (e) { location.hash = "#/"; }
    route();
    nativeReady = registerNative();
  }
  function signedOut() {
    auth = null; store.set("auth", null);
    if (window.NativeInterface) { try { localStorage.removeItem("jellyfin_credentials"); } catch (e) { /* egal */ } }
    route();
  }
  // App „Jellyfin“ für Android: Sie lädt Dateien selbst herunter und braucht
  // dafür die Anmeldung. Wie bei der Jellyfin-Weboberfläche liest sie Konto und
  // Token aus „jellyfin_credentials“, sobald die Seite /Sessions/Capabilities/Full
  // meldet. Ohne lesbaren Eintrag bricht die App die Verbindung ab – daher nur
  // melden, wenn er sicher gespeichert ist.
  async function registerNative() {
    if (!auth || !window.NativeInterface) return;
    try {
      const v = JSON.stringify({ Servers: [{ UserId: auth.user, AccessToken: auth.token, ManualAddress: location.origin + BASE }] });
      localStorage.setItem("jellyfin_credentials", v);
      if (localStorage.getItem("jellyfin_credentials") !== v) return false;
      await api("/Sessions/Capabilities/Full", { method: "POST", body: JSON.stringify({ PlayableMediaTypes: ["Video"], SupportedCommands: [], SupportsMediaControl: false }) });
      await new Promise((r) => setTimeout(r, 1500)); // die App übernimmt die Anmeldung im Hintergrund
      return true;
    } catch (e) { return false; /* ohne Downloads und Player der App weiter nutzbar */ }
  }

  $("#loginForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target), btn = $("#loginForm button");
    $("#loginErr").textContent = ""; btn.disabled = true;
    try { signedIn(await api("/Users/AuthenticateByName", { method: "POST", body: JSON.stringify({ Username: f.get("user"), Pw: f.get("pw") || "" }) })); }
    catch (e) { $("#loginErr").textContent = /Konto oder Passwort/.test(e.message) ? t("Konto oder Passwort falsch") : e.message; }
    finally { btn.disabled = false; }
  });

  // Quick Connect: Code anzeigen, in der Weboberfläche bestätigen lassen
  let qcTimer = null;
  function stopQC() { clearInterval(qcTimer); qcTimer = null; $("#qcBox").hidden = true; }
  $("#qcBtn").addEventListener("click", async () => {
    $("#loginErr").textContent = "";
    let r;
    try { r = await api("/QuickConnect/Initiate", { method: "POST" }); } catch (e) { $("#loginErr").textContent = e.message; return; }
    $("#qcCode").textContent = r.Code; $("#qcBox").hidden = false;
    clearInterval(qcTimer);
    qcTimer = setInterval(async () => {
      try {
        const s = await api("/QuickConnect/Connect?Secret=" + encodeURIComponent(r.Secret));
        if (s.Authenticated) { clearInterval(qcTimer); signedIn(await api("/Users/AuthenticateWithQuickConnect", { method: "POST", body: JSON.stringify({ Secret: r.Secret }) })); }
      } catch (e) { stopQC(); $("#loginErr").textContent = e.message; }
    }, 3000);
  });
  // Anderen Server wählen: Die App „Jellyfin“ für Android öffnet ihre
  // Serverauswahl. Die App fürs iPhone bietet dafür keinen Aufruf, dort
  // verwaltet ihr Reiter „Einstellungen“ die Server.
  const srvSelect = () => !!(window.NativeInterface && typeof window.NativeInterface.openServerSelection === "function");
  const expoApp = () => !!(window.ReactNativeWebView || window.ExpoAppInfo || (window.NativeShell && typeof window.NativeShell.downloadFile === "function"));
  function srvBtnShow() { $("#srvBtn").hidden = !(srvSelect() || expoApp()); }
  $("#srvBtn").addEventListener("click", () => {
    if (srvSelect()) { window.NativeInterface.openServerSelection(); return; }
    $("#srvHint").textContent = t("Server wechseln: in der App unten auf „Einstellungen“ tippen und dort den Server wählen oder hinzufügen.");
    $("#srvHint").hidden = false;
  });
  async function logout() { try { await api("/Sessions/Logout", { method: "POST" }); } catch (e) { /* lokal trotzdem */ } signedOut(); }
  // App „Jellyfin“ für Android: Sie hat keine eigene Leiste wie die App fürs
  // iPhone. Downloads, App-Einstellungen und Serverwahl öffnet bei Jellyfin das
  // Benutzermenü der Weboberfläche – hier daher „Menü“ statt nur „Abmelden“.
  const ni = (f) => !!(window.NativeInterface && typeof window.NativeInterface[f] === "function");
  const appMenu = () => ni("openClientSettings") && ni("openDownloadManager") && ni("openServerSelection");
  const menuOpen = () => !$("#appMenu").hidden;
  function menuClose() { $("#appMenu").hidden = true; $("#logoutBtn").removeAttribute("aria-expanded"); }
  function menuBtnSet() {
    const m = appMenu();
    $("#logoutBtn").textContent = m ? t("Menü") : t("Abmelden");
    if (m) $("#logoutBtn").setAttribute("aria-haspopup", "menu"); else $("#logoutBtn").removeAttribute("aria-haspopup");
  }
  $("#logoutBtn").addEventListener("click", (e) => {
    if (!appMenu()) { logout(); return; }
    e.stopPropagation();
    if (menuOpen()) { menuClose(); return; }
    $("#appMenu").hidden = false; $("#logoutBtn").setAttribute("aria-expanded", "true");
  });
  $("#appMenu").addEventListener("click", (e) => {
    const b = e.target.closest("[data-act]");
    if (!b) return;
    menuClose();
    const a = b.dataset.act;
    if (a === "downloads") window.NativeInterface.openDownloadManager();
    else if (a === "settings") window.NativeInterface.openClientSettings();
    else if (a === "server") window.NativeInterface.openServerSelection();
    else logout();
  });
  document.addEventListener("click", (e) => { if (menuOpen() && !e.target.closest("#appMenu")) menuClose(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && menuOpen()) menuClose(); });

  // ---------- Bausteine ----------
  const ticks = (s) => Math.floor(s * 1e7);
  const secs = (tk) => (tk || 0) / 1e7;
  const fmtTime = (s) => { s = Math.floor(s); const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60; return (h ? h + ":" + String(m).padStart(2, "0") : m) + ":" + String(x).padStart(2, "0"); };
  function img(it, type, h) {
    let id = it.Id, tag = type === "Backdrop" ? (it.BackdropImageTags || [])[0] : (it.ImageTags || {})[type];
    if (!tag && type === "Primary" && it.SeriesPrimaryImageTag) { id = it.SeriesId; tag = it.SeriesPrimaryImageTag; }
    if (!tag && type === "Backdrop" && it.ParentBackdropImageTags && it.ParentBackdropImageTags.length) { id = it.ParentBackdropItemId; tag = it.ParentBackdropImageTags[0]; }
    return tag ? `${BASE}/Items/${id}/Images/${type}?tag=${encodeURIComponent(tag)}&maxHeight=${h}` : "";
  }
  const isEp = (it) => it.Type === "Episode";
  // Untertitel einer Folge: „Serie · S01E02“
  const sub = (it) => `${it.SeriesName || ""} · S${it.ParentIndexNumber ?? "?"}E${it.IndexNumber ?? "?"}`;
  function card(it, wide) {
    const ud = it.UserData || {};
    const src = isEp(it) && wide ? img(it, "Primary", 240) || img(it, "Backdrop", 240) : img(it, "Primary", 360);
    const pct = ud.PlayedPercentage && !ud.Played ? Math.min(100, ud.PlayedPercentage) : 0;
    const name = isEp(it) && !wide ? it.SeriesName || it.Name : it.Name;
    return `<a class="card${wide ? " wide" : ""}" href="#/item/${esc(it.Id)}" data-id="${esc(it.Id)}" data-type="${esc(it.Type)}">
      <div class="pic">${src ? `<img loading="lazy" src="${esc(src)}" alt="">` : `<div class="ph">${esc(name)}</div>`}${ud.Played ? `<span class="seen" title="${esc(t("Gesehen"))}">✓</span>` : ""}${pct ? `<span class="prog"><i style="width:${pct}%"></i></span>` : ""}</div>
      <div class="t">${esc(name)}</div><div class="s">${esc(isEp(it) ? sub(it) : it.ProductionYear || "")}</div></a>`;
  }
  // Folgen aus „Weiterschauen“ starten sofort; sonst Detailansicht.
  function wireCards(root, items) {
    $$(".card", root).forEach((a) => a.addEventListener("click", (ev) => {
      const it = items.find((x) => x.Id === a.dataset.id);
      if (!it) return;
      if (a.closest("[data-resume]")) { ev.preventDefault(); play(it, ticks(secs((it.UserData || {}).PlaybackPositionTicks))); return; }
      if (isEp(it) && it.SeriesId) { ev.preventDefault(); location.hash = `#/item/${it.SeriesId}/${it.SeasonId || ""}`; }
    }));
    $$("img", root).forEach((im) => im.addEventListener("error", () => im.remove()));
  }
  // #view: Startseite, Bibliothek, Suche – bleibt beim Öffnen eines Titels
  // erhalten, damit „Zurück“ an dieselbe Stelle führt. #dview: Detailansicht.
  const view = $("#view"), dview = $("#dview");
  const fields = "Fields=PrimaryImageAspectRatio,Overview,Genres&EnableUserData=true";

  // ---------- Seiten ----------
  let views = [];
  async function chips(active) {
    if (!views.length) views = (await api(`/UserViews?userId=${auth.user}`)).Items || [];
    $("#chips").innerHTML = `<a href="#/"${active === "" ? ' class="on"' : ""}>${esc(t("Startseite"))}</a>` + views.map((v) => `<a href="#/lib/${esc(v.Id)}"${active === v.Id ? ' class="on"' : ""}>${esc(v.Name)}</a>`).join("");
  }

  async function home() {
    if (!view.firstChild) view.innerHTML = `<p class="empty">${esc(t("Lade …"))}</p>`;
    const [resume, next, ...latest] = await Promise.all([
      api(`/UserItems/Resume?userId=${auth.user}&Limit=20&MediaTypes=Video&${fields}`).catch(() => ({ Items: [] })),
      api(`/Shows/NextUp?userId=${auth.user}&Limit=20&${fields}`).catch(() => ({ Items: [] })),
      ...views.map((v) => api(`/Items/Latest?userId=${auth.user}&ParentId=${v.Id}&Limit=16&${fields}`).catch(() => [])),
    ]);
    let html = "", all = [];
    const row = (title, items, wide, attr) => { if (!items.length) return; all = all.concat(items); html += `<h2>${esc(title)}</h2><div class="row"${attr || ""}>${items.map((it) => card(it, wide)).join("")}</div>`; };
    row(t("Weiterschauen"), resume.Items || [], true, " data-resume");
    row(t("Als Nächstes"), next.Items || [], true, " data-resume");
    views.forEach((v, i) => row(t("Neu: {0}", v.Name), latest[i] || []));
    view.innerHTML = html || `<p class="empty">${esc(t("Keine Titel."))}</p>`;
    wireCards(view, all);
  }

  let libState = null;
  const libLoaded = {}; // geladene Titel je Bibliothek (für „Zurück“ an dieselbe Stelle)
  async function libPage(st, start, limit) {
    const desc = st.sort === "SortName" ? "Ascending" : "Descending";
    const r = await api(`/Items?userId=${auth.user}&ParentId=${st.id}&Recursive=true&IncludeItemTypes=${st.types}&SortBy=${st.sort},SortName&SortOrder=${desc}&StartIndex=${start}&Limit=${limit}&${fields}`);
    st.items = st.items.concat(r.Items || []); st.total = r.TotalRecordCount || 0;
  }
  // Neue Titel ab „from“ anhängen (bereits gezeigte bleiben stehen).
  function libGrid(from) {
    const st = libState, grid = $("#grid");
    if (!st.items.length) grid.innerHTML = `<p class="empty">${esc(t("Keine Titel."))}</p>`;
    else {
      const tmp = document.createElement("div");
      tmp.innerHTML = st.items.slice(from || 0).map((it) => card(it)).join("");
      wireCards(tmp, st.items);
      grid.append(...tmp.childNodes);
    }
    $("#libCount").textContent = st.total ? String(st.total) : "";
    $("#moreBox").hidden = st.items.length >= st.total;
    libLoaded[st.id] = st.items.length;
  }
  // Endlos scrollen: Kommt das Ende der Liste in Sicht (mit Vorlauf), die
  // nächsten Titel laden – ohne Knopf „Mehr laden“.
  let libObs = null, libBusy = false;
  async function libMore() {
    const st = libState, box = $("#moreBox");
    if (libBusy || !st || !box || box.hidden || st.items.length >= st.total) return;
    libBusy = true;
    box.innerHTML = `<span class="muted">${esc(t("Lädt …"))}</span>`;
    try {
      const n = st.items.length;
      await libPage(st, n, 60);
      if (st !== libState || !document.body.contains(box)) return;
      libGrid(n);
      box.innerHTML = "";
      // Ist das Ende noch in Sicht (großer Bildschirm), gleich weiterladen
      if (libObs) { libObs.unobserve(box); libObs.observe(box); }
    } catch (e) {
      if (st !== libState || !document.body.contains(box)) return;
      box.innerHTML = `<button type="button" class="ghost">${esc(t("Erneut versuchen"))}</button>`;
      box.querySelector("button").addEventListener("click", libMore);
    } finally { libBusy = false; }
  }
  async function library(id, want) {
    const v = views.find((x) => x.Id === id);
    const types = !v ? "" : v.CollectionType === "movies" ? "Movie" : v.CollectionType === "tvshows" ? "Series" : "Video";
    const st = { id, types, sort: store.get("sort") || "SortName", items: [], total: 0 };
    await libPage(st, 0, Math.max(60, want || 0));
    libState = st;
    view.innerHTML = `<div class="tools"><select id="sortSel">${[["SortName", t("A–Z")], ["DateCreated", t("Zuletzt hinzugefügt")], ["ProductionYear", t("Erscheinungsjahr")], ["CommunityRating", t("Bewertung")]]
      .map(([k, l]) => `<option value="${k}"${k === st.sort ? " selected" : ""}>${esc(l)}</option>`).join("")}</select><span class="muted" id="libCount"></span></div><div class="grid" id="grid"></div><div class="more" id="moreBox" hidden></div>`;
    $("#sortSel").addEventListener("change", () => { store.set("sort", $("#sortSel").value); library(id).then(() => window.scrollTo(0, 0), showErr); });
    libGrid();
    if (libObs) libObs.disconnect();
    if ("IntersectionObserver" in window) {
      libObs = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) libMore(); }, { rootMargin: "0px 0px 1200px 0px" });
      libObs.observe($("#moreBox"));
    } else window.onscroll = () => { const b = $("#moreBox"); if (b && b.getBoundingClientRect().top < innerHeight + 1200) libMore(); };
  }

  async function search(q) {
    $("#q").value = q;
    const r = await api(`/Items?userId=${auth.user}&SearchTerm=${encodeURIComponent(q)}&IncludeItemTypes=Movie,Series,Episode,Video&Recursive=true&Limit=60&${fields}`);
    const items = r.Items || [];
    view.innerHTML = `<h2>${esc(t("Suche: {0}", q))}</h2><div class="grid">${items.map((it) => card(it)).join("")}</div>` + (items.length ? "" : `<p class="empty">${esc(t("Keine Titel."))}</p>`);
    wireCards(view, items);
  }
  $("#searchForm").addEventListener("submit", (ev) => { ev.preventDefault(); const q = $("#q").value.trim(); if (q) location.hash = "#/search/" + encodeURIComponent(q); });

  async function detail(id, seasonId) {
    const it = await api(`/Users/${auth.user}/Items/${id}`);
    const bd = img(it, "Backdrop", 720), poster = img(it, "Primary", 480);
    const ud = it.UserData || {}, pos = secs(ud.PlaybackPositionTicks);
    const facts = [it.ProductionYear, it.OfficialRating, it.RunTimeTicks >= 6e8 ? t("{0} Min.", Math.round(secs(it.RunTimeTicks) / 60)) : "",
      it.CommunityRating ? "★ " + it.CommunityRating.toFixed(1) : "", (it.Genres || []).slice(0, 3).join(", ")].filter(Boolean);
    const ppl = it.People || [];
    const dir = ppl.filter((p) => p.Type === "Director").map((p) => p.Name), cast = ppl.filter((p) => p.Type === "Actor").slice(0, 8).map((p) => p.Name);
    dview.innerHTML = `<div class="hero"${bd ? ` style="background-image:url('${esc(bd)}')"` : ""}></div>
      <div class="detail">${poster ? `<img class="poster" src="${esc(poster)}" alt="">` : ""}<div><h1>${esc(it.Name)}</h1><div class="facts">${facts.map((f) => `<span>${esc(f)}</span>`).join("")}</div></div></div>
      <div class="actions" id="acts"></div>
      ${it.Overview ? `<p class="overview">${esc(it.Overview)}</p>` : ""}
      ${dir.length ? `<p class="people"><b>${esc(t("Regie:"))}</b> ${esc(dir.join(", "))}</p>` : ""}${cast.length ? `<p class="people"><b>${esc(t("Darsteller:"))}</b> ${esc(cast.join(", "))}</p>` : ""}
      <div id="seasonBox"></div>`;
    if (it.Type !== "Series") {
      const btn = [];
      if (pos > 30 && !ud.Played) btn.push(`<button type="button" class="primary" data-start="${ticks(pos)}">${esc(t("▶ Fortsetzen ab {0}", fmtTime(pos)))}</button>`);
      btn.push(`<button type="button" class="${btn.length ? "ghost" : "primary"}" data-start="0">${esc(t("▶ Abspielen"))}</button>`);
      if (it.CanDownload) btn.push(`<button type="button" class="ghost" id="dlBtn">${esc(t("⤓ Herunterladen"))}</button>`);
      $("#acts").innerHTML = btn.join("") + '<p class="dlmsg muted" id="dlMsg" hidden></p>';
      $$("#acts [data-start]").forEach((b) => b.addEventListener("click", () => play(it, +b.dataset.start)));
      if (it.CanDownload) { $("#dlBtn").addEventListener("click", () => download(it, $("#dlMsg"))); dlResume(it, $("#dlMsg")); }
      return;
    }
    const seasons = (await api(`/Shows/${it.Id}/Seasons?userId=${auth.user}`)).Items || [];
    if (!seasons.length) return;
    const cur = seasons.find((s) => s.Id === seasonId) || seasons.find((s) => !(s.UserData || {}).Played) || seasons[0];
    $("#seasonBox").innerHTML = `<div class="seasons">${seasons.map((s) => `<button type="button" data-season="${esc(s.Id)}"${s.Id === cur.Id ? ' class="on"' : ""}>${esc(s.Name)}</button>`).join("")}</div><div class="eps" id="eps"></div>`;
    $$("[data-season]").forEach((b) => b.addEventListener("click", () => { history.replaceState(history.state, "", `#/item/${it.Id}/${b.dataset.season}`); detail(it.Id, b.dataset.season); }));
    const eps = (await api(`/Shows/${it.Id}/Episodes?userId=${auth.user}&SeasonId=${cur.Id}&${fields}`)).Items || [];
    $("#eps").innerHTML = eps.map((e) => {
      const eu = e.UserData || {}, ep = secs(eu.PlaybackPositionTicks), src = img(e, "Primary", 240);
      const pct = eu.PlayedPercentage && !eu.Played ? Math.min(100, eu.PlayedPercentage) : 0;
      return `<div class="ep"><button type="button" class="card wide" data-ep="${esc(e.Id)}" data-start="${ep > 30 && !eu.Played ? ticks(ep) : 0}"><div class="pic">${src ? `<img loading="lazy" src="${esc(src)}" alt="">` : `<div class="ph">▶</div>`}${eu.Played ? '<span class="seen">✓</span>' : ""}${pct ? `<span class="prog"><i style="width:${pct}%"></i></span>` : ""}</div></button>
        <div class="eb"><div class="et">${esc((e.IndexNumber != null ? e.IndexNumber + ". " : "") + (e.Name || t("Folge {0}", e.IndexNumber)))}${e.CanDownload ? ` <button type="button" class="dl" data-dl="${esc(e.Id)}" title="${esc(t("⤓ Herunterladen"))}" aria-label="${esc(t("⤓ Herunterladen"))}">⤓</button>` : ""}</div><p class="dlmsg muted" data-dlmsg="${esc(e.Id)}" hidden></p>${e.RunTimeTicks >= 6e8 ? `<div class="s muted">${esc(t("{0} Min.", Math.round(secs(e.RunTimeTicks) / 60)))}${ep > 30 && !eu.Played ? " · " + esc(fmtTime(ep)) : ""}</div>` : ""}${e.Overview ? `<div class="eo">${esc(e.Overview)}</div>` : ""}</div></div>`;
    }).join("");
    $$("[data-ep]").forEach((b) => b.addEventListener("click", () => play(eps.find((e) => e.Id === b.dataset.ep), +b.dataset.start)));
    $$("[data-dl]").forEach((b) => {
      const e = eps.find((x) => x.Id === b.dataset.dl), box = $(`[data-dlmsg="${CSS.escape(e.Id)}"]`);
      b.addEventListener("click", () => download(e, box));
      dlResume(e, box);
    });
    $$("#eps img").forEach((im) => im.addEventListener("error", () => im.remove()));
    const nextEp = eps.find((e) => !(e.UserData || {}).Played);
    if (nextEp) {
      const np = secs((nextEp.UserData || {}).PlaybackPositionTicks);
      $("#acts").innerHTML = `<button type="button" class="primary">${esc(np > 30 ? t("▶ Fortsetzen ab {0}", fmtTime(np)) : t("▶ Abspielen"))} · S${nextEp.ParentIndexNumber}E${nextEp.IndexNumber}</button>`;
      $("#acts button").addEventListener("click", () => play(nextEp, np > 30 ? ticks(np) : 0));
    }
  }

  // ---------- Herunterladen ----------
  // In der App „Jellyfin“ übernimmt die App das Herunterladen (Reiter
  // „Downloads“, offline abspielbar). Ihr Player kann auf dem iPhone nur MP4 –
  // andere Dateien wandelt der Server dafür vorher um (wie bei DS video).
  const IOS = /iPhone|iPad|iPod/.test(ua) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  const nativeDl = () => !!(window.NativeShell && typeof window.NativeShell.downloadFile === "function");
  const absUrl = (p) => new URL(BASE + p, location.href).href;
  const dlName = (it, ext) => ((isEp(it) ? `${it.SeriesName} - S${it.ParentIndexNumber}E${it.IndexNumber} - ${it.Name}` : it.Name + (it.ProductionYear ? ` (${it.ProductionYear})` : "")) || "Video").replace(/[\\/:*?"<>|]+/g, " ").trim() + "." + ext;
  const needsMp4 = (it) => IOS && nativeDl() && !/^(mp4|m4v|mov)$/i.test(it.Container || "");
  // Gestartete Downloads, deren Übertragung noch läuft (Id → Startzeit).
  // Was danach in der App liegt, kann die Seite nicht sehen (Löschen in der
  // App erfährt sie nicht) – daher keine Markierung „heruntergeladen“.
  const dlPend = () => store.get("dlp:" + (auth ? auth.user : "")) || {};
  function setDl(key, id, v) { const m = store.get(key + (auth ? auth.user : "")) || {}; if (v == null) delete m[id]; else m[id] = v; store.set(key + (auth ? auth.user : ""), m); }
  const ANDROID_APP = () => !!window.NativeInterface;
  const DEV = () => (IOS ? t("aufs iPhone") : ANDROID_APP() ? t("aufs Handy") : "");
  const DEV_FOR = () => (IOS ? t("fürs iPhone") : ANDROID_APP() ? t("fürs Handy") : t("zum Herunterladen"));

  function startDownload(it, url, filename) {
    setDl("dlp:", it.Id, Date.now());
    if (nativeDl()) {
      window.NativeShell.downloadFile({ url, filename, title: it.Name, itemId: it.Id, serverId: it.ServerId,
        item: { ...it, Path: filename, Container: filename.split(".").pop() } });
      return;
    }
    const a = document.createElement("a");
    a.href = url; a.download = filename; document.body.appendChild(a); a.click(); a.remove();
  }
  function original(it) {
    const ext = (it.Container || "mkv").split(",")[0].toLowerCase();
    startDownload(it, absUrl(`/Items/${it.Id}/Download?api_key=${encodeURIComponent(auth.token)}`), dlName(it, ext));
  }
  // Meldung mit Restzeit: läuft jede Sekunde herunter und wird bei jeder
  // Abfrage an den tatsächlichen Fortschritt angepasst.
  function dlMsg(box, msg, left, extra) {
    box.hidden = false;
    box.dataset.msg = msg;
    if (left != null && left >= 0) box.dataset.until = String(Date.now() + left * 1000); else delete box.dataset.until;
    dlRender(box);
    if (extra) box.appendChild(extra);
  }
  function dlRender(box) {
    let txt = box.dataset.msg || "";
    if (box.dataset.until) {
      const left = Math.round((+box.dataset.until - Date.now()) / 1000);
      txt += " · " + (left > 0 ? t("noch {0}", fmtTime(left)) : t("gleich fertig"));
    }
    const keep = box.querySelector("button");
    box.textContent = txt;
    if (keep) { box.append(" "); box.appendChild(keep); }
  }
  setInterval(() => $$(".dlmsg[data-until]").forEach(dlRender), 1000);
  // Stand anzeigen; true = weiter abfragen. want: Nutzer hat „Herunterladen“
  // gedrückt (fertige Umwandlung dann sofort laden).
  function dlShow(it, box, st, want) {
    const tr = st.Transfer, pend = dlPend()[it.Id];
    if (pend && tr && tr.Done) {
      setDl("dlp:", it.Id, null);
      let extra = null;
      if (ANDROID_APP() && typeof window.NativeShell.openDownloadManager === "function") {
        extra = document.createElement("button"); extra.type = "button"; extra.className = "ghost"; extra.textContent = t("Downloads öffnen");
        extra.addEventListener("click", () => window.NativeShell.openDownloadManager());
      }
      dlMsg(box, "✓ " + (IOS ? t("Auf dem iPhone – unter „Downloads“ abspielbar") : ANDROID_APP() ? t("Auf dem Handy – unter „Downloads“ abspielbar") : t("Heruntergeladen")), null, extra);
      return false;
    }
    if (pend && tr && !tr.Done) {
      const pct = tr.Total > 0 ? Math.min(99, Math.floor((tr.Sent / tr.Total) * 100)) : 0;
      if (tr.Stalled) dlMsg(box, IOS ? t("Übertragung stockt bei {0} % – App im Vordergrund lassen", pct) : t("Übertragung stockt bei {0} %", pct), null);
      else dlMsg(box, (DEV() ? t("Wird {0} übertragen … {1} %", DEV(), pct) : t("Wird heruntergeladen … {0} %", pct)), tr.TimeLeft);
      return true;
    }
    if (pend && !tr) {
      if (Date.now() - pend > 120000) {
        setDl("dlp:", it.Id, null);
        // Android: Die App lädt (voreingestellt) nur im WLAN und wartet sonst.
        dlMsg(box, ANDROID_APP() ? t("Download noch nicht gestartet – die App lädt voreingestellt nur im WLAN. Stand: in der Benachrichtigung bzw. unter „Downloads“.") : t("Download nicht gestartet – bitte erneut versuchen."), null);
        return false;
      }
      if (!nativeDl()) { setDl("dlp:", it.Id, null); box.hidden = true; return false; } // Browser: eigener Download
      dlMsg(box, t("Download wird gestartet …"), null);
      return true;
    }
    if (st.State === "done") {
      if (!want) { box.hidden = true; return false; }
      startDownload(it, absUrl(`/DsvrOffline/File/${st.Id}?api_key=${encodeURIComponent(auth.token)}`), dlName(it, "mp4"));
      dlMsg(box, t("Download wird gestartet …"), null);
      return nativeDl();
    }
    if (st.State === "error" || st.State === "stop") { dlMsg(box, t("Umwandlung fehlgeschlagen: {0}", st.Error || st.State), null); return false; }
    if (st.State === "wait") { dlMsg(box, t("Wartet auf die Umwandlung …"), null); return true; }
    if (st.State === "process") {
      dlMsg(box, t("Wird {0} vorbereitet … {1} %", DEV_FOR(), Math.round((st.Progress || 0) * 100)), st.TimeLeft > 0 ? st.TimeLeft : null);
      return true;
    }
    box.hidden = true;
    return false;
  }
  const dlPolls = {};
  function dlPoll(it, box, want, delay) {
    clearTimeout(dlPolls[it.Id]);
    dlPolls[it.Id] = setTimeout(async () => {
      if (!document.body.contains(box)) return; // Ansicht verlassen
      try { if (dlShow(it, box, await api(`/DsvrOffline/${it.Id}`), want)) dlPoll(it, box, want, 2000); } catch (e) { dlPoll(it, box, want, 5000); }
    }, delay);
  }
  async function download(it, box) {
    box.dataset.item = it.Id;
    dlMsg(box, t("Lade …"), null);
    if (!needsMp4(it)) {
      original(it);
      dlMsg(box, t("Download wird gestartet …"), null);
      dlPoll(it, box, false, 1500);
      return;
    }
    try {
      const st = await api(`/DsvrOffline/${it.Id}`, { method: "POST", body: "{}" });
      setDl("dlp:", it.Id, null);
      if (dlShow(it, box, st, true)) dlPoll(it, box, true, 2000);
    } catch (e) { dlMsg(box, t("Herunterladen nicht möglich: {0}", e.message), null); }
  }
  // Ansicht wieder geöffnet: laufende Umwandlung oder Übertragung weiter zeigen
  async function dlResume(it, box) {
    box.dataset.item = it.Id;
    const pend = !!dlPend()[it.Id];
    if (!pend && !needsMp4(it)) return;
    try {
      const st = await api(`/DsvrOffline/${it.Id}`);
      if (pend || st.State === "wait" || st.State === "process") { if (dlShow(it, box, st, false)) dlPoll(it, box, false, 2000); }
    } catch (e) { /* egal */ }
  }

  // ---------- Bandbreite ----------
  // Wie Jellyfin: vor der Wiedergabe die Leitung zum Server messen. Ist sie
  // langsamer als die Datei (z.B. unterwegs, Upload des Heimanschlusses),
  // wandelt der Server mit passender Bitrate um.
  let bw = { at: 0, bps: 0 };
  async function bandwidth() {
    if (Date.now() - bw.at < 120000) return bw.bps;
    const test = async (size) => {
      const t0 = performance.now();
      const r = await fetch(`${BASE}/Playback/BitrateTest?size=${size}`, { headers: { Authorization: header() }, cache: "no-store" });
      const n = (await r.arrayBuffer()).byteLength;
      return n * 8 / Math.max((performance.now() - t0) / 1000, 0.02);
    };
    let bps = 0;
    try {
      bps = await test(500000);
      if (bps > 8e6) bps = await test(3000000);
    } catch (e) { bps = 0; }
    bw = { at: Date.now(), bps };
    return bps;
  }

  // ---------- Wiedergabe ----------
  const video = $("#video");
  let hls = null, cur = null, progT = null;
  function profile() {
    const can = (m) => video.canPlayType(m) !== "";
    // iPhone/iPad: HEVC können alle Geräte ab iOS 15 (A9) – die Abfrage im
    // Web-Fenster einer App ist nicht immer zuverlässig.
    const ios = /iPhone|iPad|iPod/.test(ua) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
    const hevc = ios || can('video/mp4; codecs="hvc1.1.6.L93.B0"'), ac3 = can('audio/mp4; codecs="ac-3"'), eac3 = can('audio/mp4; codecs="ec-3"');
    const audio = ["aac", "mp3"].concat(ac3 ? ["ac3"] : [], eac3 ? ["eac3"] : []).join(",");
    const vcodec = "h264" + (hevc ? ",hevc" : "");
    const dp = [{ Container: "mp4,m4v,mov", Type: "Video", VideoCodec: vcodec, AudioCodec: audio }];
    if (can('video/webm; codecs="vp9"')) dp.push({ Container: "webm", Type: "Video", VideoCodec: "vp8,vp9,av1", AudioCodec: "opus,vorbis" });
    return { DirectPlayProfiles: dp, TranscodingProfiles: [{ Container: "ts", Type: "Video", Protocol: "hls", VideoCodec: vcodec, AudioCodec: audio }],
      // H.264 mit 10 Bit kann kein Hardware-Decoder → umwandeln statt kopieren
      CodecProfiles: [{ Type: "Video", Codec: "h264", Conditions: [{ Condition: "LessThanEqual", Property: "VideoBitDepth", Value: "8", IsRequired: false }] }],
      SubtitleProfiles: [{ Format: "vtt", Method: "External" }] };
  }
  const report = (what, extra) => cur && api("/Sessions/Playing" + what, { method: "POST", body: JSON.stringify({ ItemId: cur.item.Id, MediaSourceId: cur.src.Id, PlaySessionId: cur.psid, PositionTicks: ticks(video.currentTime || 0), IsPaused: video.paused, ...(extra || {}) }) }).catch(() => {});

  // Klappt die Wiedergabe nicht, wird stufenweise mehr umgewandelt:
  // 0 normal · 1 ohne Direktwiedergabe · 2 Ton → AAC · 3 Bild und Ton umwandeln.
  let attempt = 0, playT0 = 0, maxBr = 0, jsTried = -1;
  // Eigener Player (hls.js über Managed Media Source, ab iOS 17.1) als
  // Rückfall, wenn der Apple-Player eine Liste nicht startet.
  const canJs = () => !!(window.Hls && window.Hls.isSupported());
  const NEXT_MSG = ["", "Direktwiedergabe fehlgeschlagen – wird umgewandelt …", "Wiedergabe fehlgeschlagen – Ton wird umgewandelt …", "Wiedergabe fehlgeschlagen – wird vollständig umgewandelt …"];
  // ---------- Player der App „Jellyfin“ für Android ----------
  // Die Android-App bringt einen eigenen Player mit (ExoPlayer, in der App
  // voreingestellt). Er spielt MKV, HEVC und 4K direkt aus der Datei und spult
  // darin – wie die DS-video-Apps. Er holt sich Wiedergabeinfos und meldet den
  // Fortschritt selbst über die Jellyfin-Schnittstelle. Ist in der App der
  // Web-Player eingestellt, spielt die Mediathek selbst.
  let nativeReady = Promise.resolve(false), followT = 0;
  const appPlayer = () => { try { return !!(window.NativePlayer && window.NativePlayer.isEnabled()); } catch (e) { return false; } };
  // Externer Player (VLC, MX Player, mpv …), eingestellt in den App-Einstellungen
  const extPlayer = () => { try { return !!(window.ExternalPlayer && window.ExternalPlayer.isEnabled()); } catch (e) { return false; } };
  // Die App holt die Wiedergabeinfo selbst und startet den Player mit der
  // direkten Adresse. Wie es ausging, meldet sie über window.ExtPlayer:
  // notifyTimeUpdate(ms) bzw. ohne Wert (bis zum Ende), dann notifyEnded();
  // notifyCanceled() bei Abbruch ohne Fortschritt. Die Seite trägt das ein.
  function playExternal(item, startTicks) {
    let pos = startTicks ? startTicks / 1e4 : 0; // ms; null = bis zum Ende gesehen
    const save = (tk) => api("/Sessions/Playing/Stopped", { method: "POST", body: JSON.stringify({ ItemId: item.Id, PositionTicks: tk }) })
      .catch(() => {}).then(() => { dirty = true; route(true); });
    window.ExtPlayer = {
      notifyTimeUpdate(ms) { pos = ms == null ? null : Number(ms); },
      notifyEnded() { save(pos == null ? (item.RunTimeTicks || 0) : Math.round(pos * 1e4)); },
      notifyCanceled() { /* nichts gesehen: Stand bleibt */ },
    };
    window.ExternalPlayer.initPlayer(JSON.stringify({ ids: [item.Id], startIndex: 0, startPositionTicks: startTicks || 0 }));
  }
  async function playNative(item, startTicks) {
    const bps = await bandwidth();
    const br = bps > 0 && bps < 40e6 ? Math.max(Math.round(bps * 0.8), 700000) : 120000000;
    window.NativePlayer.loadPlayer(JSON.stringify({ ids: [item.Id], startIndex: 0, startPositionTicks: startTicks || 0 }),
      JSON.stringify({ maxStreamingBitrateLocal: br, maxStreamingBitrateRemote: br }));
    // Die App meldet das Ende nicht an die Seite: Stand im Hintergrund nachführen
    dirty = true;
    clearInterval(followT);
    let last = null, same = 0;
    followT = setInterval(async () => {
      try {
        const u = (await api(`/Users/${auth.user}/Items/${item.Id}`)).UserData || {};
        const k = (u.PlaybackPositionTicks || 0) + ":" + !!u.Played;
        if (k === last) { if (++same >= 6) clearInterval(followT); return; }
        last = k; same = 0; dirty = true; route(true);
      } catch (e) { clearInterval(followT); }
    }, 10000);
  }

  async function play(item, startTicks, level, useJs) {
    level = level || 0;
    if (!level && !useJs && extPlayer() && await nativeReady && extPlayer()) { playExternal(item, startTicks); return; }
    if (!level && !useJs && appPlayer() && await nativeReady && appPlayer()) { playNative(item, startTicks); return; }
    const my = ++attempt;
    if (!level && !useJs) { playT0 = Date.now(); jsTried = -1; }
    await stop(false);
    $("#pTitle").textContent = isEp(item) ? `${item.SeriesName} – S${item.ParentIndexNumber}E${item.IndexNumber} ${item.Name}` : item.Name;
    if (!level) { $("#pMsg").textContent = t("Wird vorbereitet …"); $("#pDiag").hidden = true; }
    $("#player").hidden = false;
    let pi;
    try {
      const bps = await bandwidth();
      // unter ~40 Mbit/s: 80 % der gemessenen Leitung als Obergrenze
      maxBr = bps > 0 && bps < 40e6 ? Math.max(Math.round(bps * 0.8), 700000) : 0;
      pi = await api(`/Items/${item.Id}/PlaybackInfo?userId=${auth.user}`, { method: "POST",
        body: JSON.stringify({ DeviceProfile: profile(), StartTimeTicks: startTicks || 0, EnableDirectPlay: level < 1, EnableTranscoding: true,
          AllowAudioStreamCopy: level < 2, AllowVideoStreamCopy: level < 3, AutoOpenLiveStream: false, ...(maxBr ? { MaxStreamingBitrate: maxBr } : {}) }) });
    } catch (e) { $("#pMsg").textContent = t("Wiedergabe nicht möglich: {0}", e.message); return; }
    if (my !== attempt) return;
    const src = (pi.MediaSources || [])[0];
    if (!src) { $("#pMsg").textContent = t("Wiedergabe nicht möglich: {0}", pi.ErrorCode || "?"); return; }
    cur = { item, src, psid: pi.PlaySessionId, start: secs(startTicks) };
    dirty = true; // Fortschritt ändert sich: Listen beim nächsten Anzeigen neu laden
    $$("track", video).forEach((tr) => tr.remove());
    // Untertitel erst nach dem Laden des Videos anhängen: Auf dem iPhone gibt
    // WebKit beim Laden vorhandene <track>-Elemente an den Apple-Player weiter,
    // und der lehnt das ganze Video ab, wenn ein Untertitel nicht passt.
    const addTracks = () => {
      if (my !== attempt) return;
      (src.MediaStreams || []).filter((s) => s.Type === "Subtitle" && s.DeliveryMethod === "External" && s.DeliveryUrl).forEach((s) => {
        const tr = document.createElement("track");
        tr.kind = "subtitles"; tr.label = s.DisplayTitle || s.Language || "?"; if (s.Language) tr.srclang = s.Language.slice(0, 2);
        tr.src = BASE + s.DeliveryUrl.replace(/Stream\.\w+\?/, "Stream.vtt?");
        video.appendChild(tr);
      });
    };
    video.addEventListener("loadedmetadata", addTracks, { once: true });
    const begin = () => {
      if (cur && cur.start > 5 && Math.abs(video.currentTime - cur.start) > 5) video.currentTime = cur.start;
      video.play().catch(() => {});
    };
    // Fehler: nächste Stufe (Ton nur umwandeln, wenn er bisher kopiert wurde)
    const fail = (why) => {
      if (my !== attempt || !cur) return;
      clearTimeout(watch);
      const aud = (src.MediaStreams || []).find((s) => s.Type === "Audio" && s.Index === src.DefaultAudioStreamIndex) || {};
      let next = Math.max(level + 1, src.SupportsDirectPlay ? 1 : 2);
      const copied = /^(ac3|eac3)$/i.test(aud.Codec || "") && profile().TranscodingProfiles[0].AudioCodec.split(",").includes(aud.Codec.toLowerCase());
      if (next === 2 && !copied) next = 3;
      if (next > 3) {
        // Angaben für die Fehlersuche: Datei, Server-Antwort, Dauer
        const msg = t("Wiedergabe nicht möglich: {0}", why) + " (" + t("nach {0} s", Math.round((Date.now() - playT0) / 1000)) + ")\n" + t("Datei: {0}", fileInfo(src));
        $("#pMsg").textContent = msg + (src.TranscodingUrl ? "\n" + t("Prüfe Server …") : "");
        $("#pDiag").textContent = ""; $("#pDiag").hidden = true;
        const diag = () => showRequests(my, 14);
        if (src.TranscodingUrl) checkServer(BASE + src.TranscodingUrl).then((r) => { if (my === attempt) $("#pMsg").textContent = msg + "\n" + r; diag(); });
        else diag();
        return;
      }
      const pos = Math.max(cur.start, video.currentTime || 0);
      // Erst denselben Stream mit dem eigenen Player versuchen
      if (src.TranscodingUrl && !useJs && jsTried !== level && canJs()) {
        jsTried = level;
        $("#pMsg").textContent = t("Apple-Player startet nicht – eigener Player …");
        play(item, ticks(pos), level, true);
        return;
      }
      $("#pMsg").textContent = t(NEXT_MSG[next]);
      play(item, ticks(pos), next);
    };
    const mediaErr = () => { const e = video.error; return e ? e.message || "MEDIA_ERR " + e.code : "?"; };
    let watch = 0;
    if (src.SupportsDirectPlay) {
      $("#pMsg").textContent = "";
      video.src = `${BASE}/Videos/${item.Id}/stream?static=true&MediaSourceId=${encodeURIComponent(src.Id)}&api_key=${encodeURIComponent(auth.token)}`;
      video.addEventListener("loadedmetadata", begin, { once: true });
      video.addEventListener("error", () => fail(mediaErr()), { once: true });
    } else if (src.TranscodingUrl) {
      $("#pMsg").textContent = t(level >= 3 ? "Vollständig umgewandelt" : "Umgewandelt") + (useJs ? " · " + t("eigener Player") : "") + (maxBr ? " · " + t("Leitung ca. {0} Mbit/s", (bw.bps / 1e6).toFixed(1).replace(".", de ? "," : ".")) : "");
      // Übersicht (master.m3u8) mit CODECS, Auflösung und VIDEO-RANGE – wie
      // bei Jellyfin. Lädt der Apple-Player die Segmentliste direkt, fragt er
      // fMP4-Segmente (HEVC/4K) endlos ab.
      const url = BASE + src.TranscodingUrl;
      if (!useJs && video.canPlayType("application/vnd.apple.mpegurl")) {
        video.disableRemotePlayback = false;
        video.src = url; video.addEventListener("loadedmetadata", begin, { once: true });
        video.addEventListener("error", () => fail(mediaErr()), { once: true });
      } else if (canJs()) {
        video.disableRemotePlayback = !!useJs; // Managed Media Source verlangt das ohne AirPlay-Quelle
        hls = new window.Hls({ preferManagedMediaSource: true, maxBufferLength: 30, maxMaxBufferLength: 60, backBufferLength: 30, startPosition: cur.start > 0 ? cur.start : -1 });
        hls.on(window.Hls.Events.ERROR, (_, d) => { if (d.fatal) fail(d.details); });
        hls.loadSource(url); hls.attachMedia(video);
        hls.on(window.Hls.Events.MANIFEST_PARSED, () => video.play().catch(() => {}));
      } else { $("#pMsg").textContent = t("Dieses Gerät kann HLS nicht abspielen."); return; }
      // Läuft die Wiedergabe nicht an (z.B. Liste wird endlos geladen), den
      // eigenen Player bzw. die nächste Stufe versuchen. Vollständiges
      // Umwandeln braucht auf der NAS länger.
      watch = setTimeout(() => { if (video.readyState < 3 && !(video.currentTime > cur.start + 0.5)) fail(t("Zeitüberschreitung")); }, level >= 3 ? 60000 : 25000);
      video.addEventListener("playing", () => clearTimeout(watch), { once: true });
    } else { $("#pMsg").textContent = t("Wiedergabe nicht möglich: {0}", "—"); return; }
    report("", { CanSeek: true });
    clearInterval(progT);
    progT = setInterval(() => report("/Progress"), 10000);
    // Lädt es länger als 10 s, die Abrufe des Players laufend anzeigen
    const t1 = Date.now();
    const slow = () => {
      if (my !== attempt || !cur || $("#player").hidden) return;
      if (video.readyState >= 3 || !video.paused && video.currentTime > 0.5) { $("#pDiag").hidden = true; return; }
      if (Date.now() - t1 > 10000) showRequests(my, 10, t("Lädt …") + ` ${Math.round((Date.now() - t1) / 1000)} s · ` + fileInfo(src));
      setTimeout(slow, 4000);
    };
    setTimeout(slow, 4000);
  }

  // Letzte Abrufe der Wiedergabe (Server-Protokoll) unter der Meldung zeigen
  function showRequests(my, n, head) {
    return api("/DsvrDiag/Requests").then((list) => {
      if (my !== attempt || !Array.isArray(list)) return;
      const tm = (d) => new Date(d).toTimeString().slice(0, 8);
      const kb = (b) => b >= 1e6 ? (b / 1e6).toFixed(1) + " MB" : Math.round(b / 1e3) + " kB";
      const lines = list.slice(-n).map((e) => `${tm(e.At)} ${e.Path.split("/").pop()}${e.Range ? " [" + e.Range.replace("bytes=", "") + "]" : ""} → ${e.Status} ${kb(e.Bytes)} ${e.Ms} ms ${e.Agent}`);
      $("#pDiag").textContent = (head ? head + "\n" : "") + lines.join("\n");
      $("#pDiag").hidden = false;
    }).catch(() => {});
  }

  // Kurzbeschreibung der Datei (Video/Ton) für Fehlermeldungen
  function fileInfo(src) {
    const s = src.MediaStreams || [];
    const v = s.find((x) => x.Type === "Video") || {};
    const a = s.find((x) => x.Type === "Audio" && x.Index === src.DefaultAudioStreamIndex) || s.find((x) => x.Type === "Audio") || {};
    const vid = [v.Codec, v.Profile, v.Width && v.Height ? v.Width + "×" + v.Height : "", v.PixelFormat].filter(Boolean).join(" ");
    return [vid, [a.Codec, a.Channels ? a.Channels + "ch" : ""].filter(Boolean).join(" "), src.Container].filter(Boolean).join(" · ");
  }
  // Fragt die Umwandlung selbst ab (Liste, init.mp4, erstes Segment): zeigt,
  // ob der Server liefert und wie schnell.
  async function checkServer(url) {
    const ctl = new AbortController(), tm = setTimeout(() => ctl.abort(), 45000), t0 = Date.now();
    const get = async (u) => { const r = await fetch(u, { signal: ctl.signal, cache: "no-store" }); return { r, body: await r.text() }; };
    try {
      let { r, body } = await get(url);
      if (!r.ok) return t("Server: {0}", `m3u8 HTTP ${r.status} ${body.trim().slice(0, 160)}`);
      let vu = new URL(url, location.href);
      if (body.includes("#EXT-X-STREAM-INF")) { // Übersicht → Segmentliste
        const variant = body.split("\n").find((l) => l && !l.startsWith("#"));
        if (!variant) return t("Server: {0}", "master ohne Liste");
        vu = new URL(variant, vu);
        ({ r, body } = await get(vu.href));
        if (!r.ok) return t("Server: {0}", `index HTTP ${r.status} ${body.trim().slice(0, 160)}`);
      }
      const map = (body.match(/#EXT-X-MAP:URI="([^"]+)"/) || [])[1];
      const seg = body.split("\n").find((l) => l && !l.startsWith("#"));
      const parts = [];
      for (const f of [map, seg].filter(Boolean)) {
        const res = await fetch(new URL(f, vu).href, { signal: ctl.signal, cache: "no-store" });
        const n = (await res.arrayBuffer()).byteLength;
        if (!res.ok) return t("Server: {0}", `${f.split("/").pop()} HTTP ${res.status}`);
        parts.push(`${f.split("/").pop()} ${n >= 1e6 ? (n / 1e6).toFixed(1) + " MB" : n + " B"} ${((Date.now() - t0) / 1000).toFixed(1)} s`);
      }
      return t("Server: {0}", "ok – " + parts.join(", "));
    } catch (e) {
      return t("Server: {0}", e.name === "AbortError" ? t("Zeitüberschreitung") : e.message);
    } finally { clearTimeout(tm); }
  }

  async function stop(close) {
    clearInterval(progT);
    if (cur) { await report("/Stopped"); }
    cur = null;
    if (hls) { hls.destroy(); hls = null; }
    video.pause(); video.removeAttribute("src"); video.load();
    // Seite unter dem Player aktualisieren, dann Player schließen (kein Springen)
    if (close) { await route(true); $("#player").hidden = true; }
  }
  $("#pClose").addEventListener("click", () => stop(true));
  video.addEventListener("pause", () => report("/Progress"));
  window.addEventListener("pagehide", () => { if (cur) report("/Stopped"); });

  // ---------- Navigation ----------
  // Jeder Verlaufseintrag bekommt seine Tiefe (history.state.d). Damit weiß
  // „Zurück“, ob es innerhalb der Mediathek einen Schritt zurück gibt.
  let depth = 0, pendingDepth = null, firstRoute = true, routeSeq = 0;
  let shown = null;    // angezeigte Seite (Schlüssel)
  let listHash = null; // Liste, deren Inhalt in #view steht
  let dirty = false;   // nach einer Wiedergabe: Listen neu laden
  const scrollMem = {};
  function trackDepth() {
    const st = history.state;
    let fresh = false;
    if (st && typeof st.d === "number") depth = st.d;
    else {
      fresh = !firstRoute;
      depth = pendingDepth != null ? pendingDepth : firstRoute ? 0 : depth + 1;
      try { history.replaceState({ ...(st || {}), d: depth }, ""); } catch (e) { /* egal */ }
    }
    pendingDepth = null; firstRoute = false;
    return fresh;
  }
  function showErr(e) {
    if (!e || e.message === "401") return;
    const box = dview.hidden ? view : dview;
    box.innerHTML = `<p class="empty">${esc(e.message)}</p>`;
  }

  async function route(keep) {
    const loggedIn = !!auth, seq = ++routeSeq;
    $("#login").hidden = loggedIn; $("#bar").hidden = !loggedIn; $("#chips").hidden = !loggedIn;
    menuClose(); if (loggedIn) menuBtnSet();
    if (!loggedIn) {
      view.innerHTML = ""; dview.innerHTML = ""; dview.hidden = true; view.hidden = false; listHash = shown = null;
      srvBtnShow(); setTimeout(srvBtnShow, 1500); // Skripte der App kommen teils erst nach dem Laden
      try { const info = await api("/System/Info/Public"); $("#srvName").textContent = info.ServerName || ""; } catch (e) { /* egal */ }
      return;
    }
    const y = window.scrollY, prevDepth = depth;
    if (shown) scrollMem[prevDepth + shown] = y;
    const fresh = trackDepth();
    const parts = location.hash.replace(/^#!?\/?/, "").split("/").map(decodeURIComponent);
    const kind = parts[1] && ["lib", "item", "search"].includes(parts[0]) ? parts[0] : "";
    const key = kind ? `#/${kind}/${encodeURIComponent(parts[1])}` : "#/";
    const pos = keep ? y : fresh ? 0 : scrollMem[depth + key] || 0;
    $("#backBtn").hidden = kind !== "item"; $(".brand").hidden = kind === "item";
    try {
      await chips(kind === "lib" ? parts[1] : kind ? null : "");
      if (kind === "item") {
        await detail(parts[1], parts[2]);
        if (seq !== routeSeq) return;
        view.hidden = true; dview.hidden = false;
      } else {
        if (listHash !== key || dirty) {
          if (kind === "lib") await library(parts[1], fresh ? 0 : libLoaded[parts[1]]);
          else if (kind === "search") await search(parts[1]);
          else await home();
          if (seq !== routeSeq) return;
          listHash = key; dirty = false;
        }
        dview.hidden = true; dview.innerHTML = ""; view.hidden = false;
      }
      shown = key;
      window.scrollTo(0, pos);
    } catch (e) {
      if (e.message === "401") return;
      if (kind === "item") { view.hidden = true; dview.hidden = false; }
      else { dview.hidden = true; view.hidden = false; listHash = null; }
      shown = key;
      showErr(e);
    }
  }
  window.addEventListener("hashchange", () => { if ($("#player").hidden) route(); });

  // Zurück: einen Schritt im Verlauf, ohne Verlauf zur Startseite.
  const canGoBack = () => !!auth && (!$("#player").hidden || depth > 0 || (shown || "#/") !== "#/");
  function goBack() {
    if (!$("#player").hidden) { stop(true); return; }
    if (depth > 0) history.back();
    else if ((shown || "#/") !== "#/") { pendingDepth = 0; location.replace("#/"); }
  }
  function goHome() {
    if (!$("#player").hidden) { stop(false); $("#player").hidden = true; }
    if (location.hash.replace(/^#!?\/?/, "") === "") route(); else location.hash = "#/";
  }
  $("#backBtn").addEventListener("click", goBack);
  // Die App „Jellyfin“ (iPhone) ruft für den Tab „Startseite“ den Router der
  // Jellyfin-Weboberfläche auf (window.Emby.Page).
  window.Emby = window.Emby || {};
  window.Emby.Page = { canGoBack, back: goBack, goHome };
  // Android: Zurück-Taste → window.NavigationHelper.goBack() (sonst ein Plugin
  // der Jellyfin-Weboberfläche); ganz oben beendet sie die App. Die Knöpfe der
  // Benachrichtigung steuern über playbackManager den Player.
  window.NavigationHelper = {
    goBack() {
      if (menuOpen()) menuClose();
      else if (canGoBack()) goBack();
      else if (window.NativeInterface && typeof window.NativeInterface.exitApp === "function") window.NativeInterface.exitApp();
    },
    playbackManager: {
      playPause() { if (video.paused) video.play().catch(() => {}); else video.pause(); },
      play() { video.play().catch(() => {}); }, pause() { video.pause(); },
      stop() { if (!$("#player").hidden) stop(true); },
      seekMs(ms) { video.currentTime = ms / 1000; },
      sendCommand(c) { if (c && c.Name === "SetVolume" && c.Arguments) video.volume = Math.min(1, Math.max(0, c.Arguments.Volume / 100)); },
      nextTrack() {}, previousTrack() {}, fastForward() { video.currentTime += 30; }, rewind() { video.currentTime -= 10; },
    },
  };

  route();
  nativeReady = registerNative();
})();
