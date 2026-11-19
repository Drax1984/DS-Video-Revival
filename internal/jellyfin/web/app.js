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
    "Mit Quick Connect anmelden": "Sign in with Quick Connect",
    "Code in der Weboberfläche von DS Video Revival unter „Jellyfin-Apps“ bestätigen:": "Confirm this code in the DS Video Revival web interface under “Jellyfin apps”:",
    "Wartet auf Bestätigung …": "Waiting for confirmation …", "Schließen": "Close", "Startseite": "Home",
    "Weiterschauen": "Continue watching", "Als Nächstes": "Next up", "Neu: {0}": "New: {0}", "Keine Titel.": "No titles.",
    "A–Z": "A–Z", "Zuletzt hinzugefügt": "Recently added", "Erscheinungsjahr": "Release year", "Bewertung": "Rating",
    "Mehr laden": "Load more", "Suche: {0}": "Search: {0}", "▶ Abspielen": "▶ Play", "▶ Fortsetzen ab {0}": "▶ Resume from {0}",
    "Staffel {0}": "Season {0}", "Folge {0}": "Episode {0}", "{0} Min.": "{0} min", "Regie:": "Director:", "Darsteller:": "Cast:",
    "Wird vorbereitet …": "Preparing …", "Wiedergabe nicht möglich: {0}": "Playback not possible: {0}",
    "Direktwiedergabe fehlgeschlagen – wird umgewandelt …": "Direct play failed – transcoding …",
    "Dieses Gerät kann HLS nicht abspielen.": "This device cannot play HLS.", "Gesehen": "Watched",
    "Konto oder Passwort falsch": "Wrong account or password", "Server nicht erreichbar": "Server not reachable",
    "Lade …": "Loading …", "Folgen": "Episodes", "Umgewandelt": "Transcoded", "Zurück": "Back",
    "Wiedergabe fehlgeschlagen – Ton wird umgewandelt …": "Playback failed – converting audio …",
    "Wiedergabe fehlgeschlagen – wird vollständig umgewandelt …": "Playback failed – fully transcoding …",
    "Vollständig umgewandelt": "Fully transcoded", "Zeitüberschreitung": "Timeout",
    "Datei: {0}": "File: {0}", "Server: {0}": "Server: {0}", "nach {0} s": "after {0} s", "Prüfe Server …": "Checking server …",
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
  }
  function signedOut() { auth = null; store.set("auth", null); route(); }

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
  $("#logoutBtn").addEventListener("click", async () => { try { await api("/Sessions/Logout", { method: "POST" }); } catch (e) { /* lokal trotzdem */ } signedOut(); });

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
  function libGrid() {
    const st = libState;
    $("#grid").innerHTML = st.items.map((it) => card(it)).join("") || `<p class="empty">${esc(t("Keine Titel."))}</p>`;
    $("#libCount").textContent = st.total ? String(st.total) : "";
    $("#moreBtn").hidden = st.items.length >= st.total;
    wireCards($("#grid"), st.items);
    libLoaded[st.id] = st.items.length;
  }
  async function library(id, want) {
    const v = views.find((x) => x.Id === id);
    const types = !v ? "" : v.CollectionType === "movies" ? "Movie" : v.CollectionType === "tvshows" ? "Series" : "Video";
    const st = { id, types, sort: store.get("sort") || "SortName", items: [], total: 0 };
    await libPage(st, 0, Math.max(60, want || 0));
    libState = st;
    view.innerHTML = `<div class="tools"><select id="sortSel">${[["SortName", t("A–Z")], ["DateCreated", t("Zuletzt hinzugefügt")], ["ProductionYear", t("Erscheinungsjahr")], ["CommunityRating", t("Bewertung")]]
      .map(([k, l]) => `<option value="${k}"${k === st.sort ? " selected" : ""}>${esc(l)}</option>`).join("")}</select><span class="muted" id="libCount"></span></div><div class="grid" id="grid"></div><button type="button" class="ghost more" id="moreBtn" hidden>${esc(t("Mehr laden"))}</button>`;
    $("#sortSel").addEventListener("change", () => { store.set("sort", $("#sortSel").value); library(id).then(() => window.scrollTo(0, 0), showErr); });
    $("#moreBtn").addEventListener("click", async (ev) => {
      ev.target.disabled = true;
      try { await libPage(libState, libState.items.length, 60); libGrid(); } catch (e) { showErr(e); } finally { ev.target.disabled = false; }
    });
    libGrid();
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
      $("#acts").innerHTML = btn.join("");
      $$("#acts [data-start]").forEach((b) => b.addEventListener("click", () => play(it, +b.dataset.start)));
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
        <div class="eb"><div class="et">${esc((e.IndexNumber != null ? e.IndexNumber + ". " : "") + (e.Name || t("Folge {0}", e.IndexNumber)))}</div>${e.RunTimeTicks >= 6e8 ? `<div class="s muted">${esc(t("{0} Min.", Math.round(secs(e.RunTimeTicks) / 60)))}${ep > 30 && !eu.Played ? " · " + esc(fmtTime(ep)) : ""}</div>` : ""}${e.Overview ? `<div class="eo">${esc(e.Overview)}</div>` : ""}</div></div>`;
    }).join("");
    $$("[data-ep]").forEach((b) => b.addEventListener("click", () => play(eps.find((e) => e.Id === b.dataset.ep), +b.dataset.start)));
    $$("#eps img").forEach((im) => im.addEventListener("error", () => im.remove()));
    const nextEp = eps.find((e) => !(e.UserData || {}).Played);
    if (nextEp) {
      const np = secs((nextEp.UserData || {}).PlaybackPositionTicks);
      $("#acts").innerHTML = `<button type="button" class="primary">${esc(np > 30 ? t("▶ Fortsetzen ab {0}", fmtTime(np)) : t("▶ Abspielen"))} · S${nextEp.ParentIndexNumber}E${nextEp.IndexNumber}</button>`;
      $("#acts button").addEventListener("click", () => play(nextEp, np > 30 ? ticks(np) : 0));
    }
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
  let attempt = 0, playT0 = 0;
  const NEXT_MSG = ["", "Direktwiedergabe fehlgeschlagen – wird umgewandelt …", "Wiedergabe fehlgeschlagen – Ton wird umgewandelt …", "Wiedergabe fehlgeschlagen – wird vollständig umgewandelt …"];
  async function play(item, startTicks, level) {
    level = level || 0;
    const my = ++attempt;
    if (!level) playT0 = Date.now();
    await stop(false);
    $("#pTitle").textContent = isEp(item) ? `${item.SeriesName} – S${item.ParentIndexNumber}E${item.IndexNumber} ${item.Name}` : item.Name;
    if (!level) { $("#pMsg").textContent = t("Wird vorbereitet …"); $("#pDiag").hidden = true; }
    $("#player").hidden = false;
    let pi;
    try {
      pi = await api(`/Items/${item.Id}/PlaybackInfo?userId=${auth.user}`, { method: "POST",
        body: JSON.stringify({ DeviceProfile: profile(), StartTimeTicks: startTicks || 0, EnableDirectPlay: level < 1, EnableTranscoding: true,
          AllowAudioStreamCopy: level < 2, AllowVideoStreamCopy: level < 3, AutoOpenLiveStream: false }) });
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
        const diag = () => api("/DsvrDiag/Requests").then((list) => {
          if (my !== attempt || !Array.isArray(list) || !list.length) return;
          const tm = (d) => new Date(d).toTimeString().slice(0, 8);
          $("#pDiag").textContent = list.slice(-14).map((e) => `${tm(e.At)} ${e.Method} ${e.Path.split("/").slice(-2).join("/")}${e.Range ? " [" + e.Range.replace("bytes=", "") + "]" : ""} → ${e.Status} ${e.Bytes} B ${e.Agent}`).join("\n");
          $("#pDiag").hidden = false;
        }).catch(() => {});
        if (src.TranscodingUrl) checkServer(BASE + src.TranscodingUrl.replace("/master.m3u8?", "/main.m3u8?")).then((r) => { if (my === attempt) $("#pMsg").textContent = msg + "\n" + r; diag(); });
        else diag();
        return;
      }
      const pos = Math.max(cur.start, video.currentTime || 0);
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
      $("#pMsg").textContent = t(level >= 3 ? "Vollständig umgewandelt" : "Umgewandelt");
      // Direkt die Segmentliste (main.m3u8) statt der Übersicht (master.m3u8) –
      // so wie die DS-video-Apps sie bekommen.
      const url = BASE + src.TranscodingUrl.replace("/master.m3u8?", "/main.m3u8?");
      if (video.canPlayType("application/vnd.apple.mpegurl")) {
        video.src = url; video.addEventListener("loadedmetadata", begin, { once: true });
        video.addEventListener("error", () => fail(mediaErr()), { once: true });
      } else if (window.Hls && window.Hls.isSupported()) {
        hls = new window.Hls({ maxBufferLength: 60, startPosition: cur.start > 0 ? cur.start : -1 });
        hls.on(window.Hls.Events.ERROR, (_, d) => { if (d.fatal) fail(d.details); });
        hls.loadSource(url); hls.attachMedia(video);
        hls.on(window.Hls.Events.MANIFEST_PARSED, () => video.play().catch(() => {}));
      } else { $("#pMsg").textContent = t("Dieses Gerät kann HLS nicht abspielen."); return; }
      // Startet die Wiedergabe gar nicht, ebenfalls die nächste Stufe versuchen
      watch = setTimeout(() => { if (video.readyState < 1) fail(t("Zeitüberschreitung")); }, 60000);
      video.addEventListener("loadedmetadata", () => clearTimeout(watch), { once: true });
    } else { $("#pMsg").textContent = t("Wiedergabe nicht möglich: {0}", "—"); return; }
    report("", { CanSeek: true });
    clearInterval(progT);
    progT = setInterval(() => report("/Progress"), 10000);
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
    if (!loggedIn) {
      view.innerHTML = ""; dview.innerHTML = ""; dview.hidden = true; view.hidden = false; listHash = shown = null;
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
  // Die App „Jellyfin“ ruft für die Zurück-Taste (Android) und den Tab
  // „Startseite“ den Router der Jellyfin-Weboberfläche auf (window.Emby.Page).
  window.Emby = window.Emby || {};
  window.Emby.Page = { canGoBack, back: goBack, goHome };

  route();
})();
