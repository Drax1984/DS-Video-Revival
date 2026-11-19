"use strict";
(function () {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  const t = window.I18N.t, tc = window.I18N.tc, L = window.I18N.locale;
  window.I18N.apply(document.body);
  let sid = "";
  try { sid = sessionStorage.getItem("dsvr_sid") || ""; } catch (e) { /* kein Speicher */ }
  let me = null, cfg = null, kind = "movie", libId = 0, lib = [], current = null, playing = null;
  const isApple = /iPhone|iPad|iPod|Macintosh/.test(navigator.userAgent) && !!document.createElement("video").canPlayType("application/vnd.apple.mpegurl");
  const client = isApple ? "apple" : "browser";

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const saveSid = (v) => { sid = v; try { v ? sessionStorage.setItem("dsvr_sid", v) : sessionStorage.removeItem("dsvr_sid"); } catch (e) {} };
  const fmtTime = (s) => { s = Math.floor(s || 0); const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60; return (h ? h + ":" + String(m).padStart(2, "0") : m) + ":" + String(x).padStart(2, "0"); };
  const fmtSize = (b) => b > 1e9 ? (b / 1e9).toFixed(1) + " GB" : (b / 1e6).toFixed(0) + " MB";
  const planLabel = { direct: t("Direktwiedergabe"), remux: t("Umverpacken (HLS)"), transcode: t("Umwandeln nach H.264") };

  async function api(path, opts = {}) {
    const r = await fetch("/dsvr/api/" + path, { ...opts, headers: { "X-DSVR-SID": sid, "X-DSVR-SYNOTOKEN": synoToken(), "Content-Type": "application/json", ...(opts.headers || {}) } });
    if (r.status === 401) { showLogin(); throw new Error(NOT_LOGGED_IN); }
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(t(j.error || r.statusText));
    return j;
  }

  const NOT_LOGGED_IN = t("Nicht angemeldet");
  // DSM-Sicherheitstoken (SynoToken) – nur für DSM-Verwaltungsaufrufe wie den Zertifikatsexport.
  function synoToken() { try { return sessionStorage.getItem("dsvr_synotoken") || ""; } catch (e) { return ""; } }
  function saveSynoToken(t) { try { if (t) sessionStorage.setItem("dsvr_synotoken", t); } catch (e) {} }
  function showLogin(msg) { $("#logout").hidden = true; $("#tabs").hidden = true; $$("[data-panel]").forEach((p) => (p.hidden = true)); $("#login").hidden = false; $("#who").textContent = ""; if (msg) $("#loginErr").textContent = msg; }

  // Anmelden: Knopf während der Anmeldung gesperrt – mehrfaches Klicken (oder
  // Enter) erzeugte sonst mehrere Sitzungen (doppelt unter „Verbindungen“).
  let loggingIn = false;
  $("#loginForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (loggingIn) return;
    const btn = $("#loginForm button[type=submit]");
    loggingIn = true; btn.disabled = true; btn.textContent = t("Anmelden …");
    try { await login(ev); } finally { loggingIn = false; btn.disabled = false; btn.textContent = t("Anmelden"); }
  });
  async function login(ev) {
    const f = new FormData(ev.target);
    const body = new URLSearchParams({ api: "SYNO.API.Auth", version: "6", method: "login", session: "DSVideoRevival", format: "sid", account: f.get("account"), passwd: f.get("passwd") });
    if (f.get("otp_code")) body.set("otp_code", f.get("otp_code"));
    $("#loginErr").textContent = "";
    try {
      const j = await (await fetch("/webapi/entry.cgi", { method: "POST", body })).json();
      if (!j.success) {
        const code = j.error && j.error.code;
        if (code === 403) { $("#otpRow").hidden = false; $("#loginErr").textContent = t("Bitte 2FA-Code eingeben."); return; }
        $("#loginErr").textContent = code === 400 ? t("Konto oder Passwort falsch.")
          : code === 402 ? t("Keine Berechtigung für DS Video Revival (DSM → Systemsteuerung → Anwendungsberechtigungen).")
          : t("Anmeldung fehlgeschlagen (Code {0}).", code);
        return;
      }
      saveSid(j.data.sid);
      saveSynoToken(j.data.synotoken);
      await start();
    } catch (e) { $("#loginErr").textContent = t("Server nicht erreichbar."); }
  }

  $("#logout").addEventListener("click", async () => {
    const body = new URLSearchParams({ api: "SYNO.API.Auth", version: "6", method: "logout", session: "DSVideoRevival", _sid: sid });
    try { await fetch("/webapi/entry.cgi", { method: "POST", body }); } catch (e) { /* trotzdem lokal abmelden */ }
    saveSid(""); try { sessionStorage.removeItem("dsvr_synotoken"); } catch (e) {}
    me = null; showLogin(t("Abgemeldet."));
  });

  function showTab(name) {
    $$("#tabs button").forEach((x) => x.classList.toggle("active", x.dataset.tab === name));
    $$("[data-panel]").forEach((p) => (p.hidden = p.dataset.panel !== name));
    ({ library: loadLibrary, tree: loadTree, status: loadStatus, folders: loadFolders, access: loadAccess, conns: loadConns, jellyfin: loadJellyfin, settings: loadSettings, log: loadLog }[name] || (() => {}))();
  }
  $$("#tabs button").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));

  // ---------- Mediathek ----------
  // Reiter: Standardbibliotheken (id 0) und eigene Bibliotheken
  const stdLibs = [["movie", t("Filme")], ["tvshow", t("Serien")], ["home_video", t("Heimvideos")], ["tv_record", t("TV-Aufnahmen")]];
  function buildKindTabs() {
    const libs = (me && me.libraries) || [];
    if (!libs.some((l) => l.id === libId && l.type === kind) && libId !== 0) { kind = "movie"; libId = 0; }
    $("#kindSeg").innerHTML = stdLibs.map(([t, l]) => `<button data-kind="${t}" data-lib="0">${l}</button>`).join("")
      + libs.map((l) => `<button data-kind="${esc(l.type)}" data-lib="${l.id}">${esc(l.title)}</button>`).join("");
    $$("#kindSeg button").forEach((b) => {
      b.classList.toggle("active", b.dataset.kind === kind && +b.dataset.lib === libId);
      b.addEventListener("click", () => {
        kind = b.dataset.kind; libId = +b.dataset.lib;
        $$("#kindSeg button").forEach((x) => x.classList.toggle("active", x === b)); loadLibrary();
      });
    });
  }
  let searchT;
  $("#search").addEventListener("input", () => { clearTimeout(searchT); searchT = setTimeout(renderGrid, 150); });
  $("#sort").addEventListener("change", loadLibrary);

  async function loadLibrary() {
    lib = await api("library?kind=" + kind + "&library=" + libId + "&sort=" + $("#sort").value);
    try { me = await api("me"); } catch (e) { /* bleibt */ }
    renderGrid();
    const hint = $("#libHint");
    const msgs = [];
    hint.classList.toggle("blocked", !!(me && me.blocked));
    clearTimeout(blockT);
    if (me && me.blocked) {
      const b = me.blocked;
      if (b.banned_until) msgs.push(esc(t("Dieses Gerät wurde vom Administrator getrennt und ist bis {0} Uhr gesperrt. Danach erscheinen die Medien hier wieder.", connTime(b.banned_until))));
      else msgs.push(esc(t("Dein Konto ist gerade auf einem anderen Gerät aktiv{0}. Gleichzeitiges Schauen auf mehreren Geräten ist für dieses Konto nicht freigegeben – sobald dort {1} Minuten nichts mehr läuft oder abgemeldet wird, erscheinen die Medien hier wieder.", b.device ? ` (${b.device})` : "", b.idle_minutes || 5)));
      blockT = setTimeout(() => { if (!document.querySelector('[data-panel="library"]').hidden && $("#detail").hidden && $("#player").hidden) loadLibrary(); }, 30000);
    }
    if (me && me.meta && me.meta.running) msgs.push(esc(t("Poster, Inhalte und IMDb-Bewertungen werden geladen ({0}/{1}) – die Ansicht füllt sich nach und nach.", me.meta.done, me.meta.total)));
    if (me && me.ffmpeg && me.admin && (!me.aac || !me.h264)) msgs.push(esc(t("Das gefundene FFmpeg kann {0} erzeugen. iPhone/Apple TV spielen H.264-Filme mit AC3-Ton trotzdem ab (Ton wird unverändert durchgereicht); für DTS-Ton, HEVC im Browser und die Wiedergabe hier im Browser bitte Paket „ffmpeg7“ (SynoCommunity) installieren.",
      [!me.aac ? t("kein AAC") : "", !me.h264 ? t("kein H.264") : ""].filter(Boolean).join(t(" und ")))));
    if (me && !me.ffmpeg) msgs.push(esc(t("FFmpeg nicht gefunden: MKV/AVI sowie AC3/DTS-Ton können nicht umgewandelt werden. Abhilfe: Paket „ffmpeg7“ (SynoCommunity) installieren.")));
    hint.innerHTML = msgs.join("<br>"); hint.hidden = !msgs.length;
    clearTimeout(metaT);
    if (me && me.meta && me.meta.running) metaT = setTimeout(async () => {
      try { me = await api("me"); } catch (e) { return; }
      if (!$("#detail").hidden || !$("#player").hidden) return;
      if (!document.querySelector('[data-panel="library"]').hidden) loadLibrary();
    }, 15000);
  }
  let metaT, blockT;

  function renderGrid() {
    const q = $("#search").value.trim().toLowerCase();
    const list = q ? lib.filter((e) => (e.title + " " + (e.genres || []).join(" ")).toLowerCase().includes(q)) : lib;
    $("#libCount").textContent = t("{0} Titel", list.length);
    $("#grid").innerHTML = list.map((e) => {
      const fmtR = (v) => (v % 1 ? v.toFixed(1) : String(v));
      // Ein Sternchen-Schild: eigene Bewertung (blau), sonst IMDb-Empfehlung (gelb).
      const rec = e.imdb_rating || e.rating;
      const badge = e.my_rating ? `<span class="rate mine" title="${esc(t("Deine Bewertung"))}">★ ${fmtR(e.my_rating)}</span>`
        : (rec ? `<span class="rate" title="${esc(t("IMDb-Bewertung (Empfehlung)"))}">★ ${rec.toFixed(1)}</span>` : "");
      const sub = (e.kind === "tv_record" ? [fmtRec(e.recorded) || e.year, e.channel]
        : [e.year, e.kind === "tvshow" && e.episodes ? t("{0} Folgen", e.episodes) : "", (e.genres || []).slice(0, 2).join(", ")]).filter(Boolean).join(" · ");
      // Info (nur DSM-Administratoren): Dateipfad anzeigen und kopieren
      const info = me && me.dsm_admin && e.paths && e.paths.length ? `<button type="button" class="info" data-info="${e.kind}:${e.id}" aria-label="${esc(t("Dateipfad von {0}", e.title))}">Info</button>` : "";
      return `<div class="tw"><button class="tile" data-id="${e.id}" data-kind="${e.kind}"><div class="pimg">${e.poster ? `<img loading="lazy" src="${esc(e.poster)}" alt="">` : `<div class="ph">${esc(e.title)}</div>`}${badge}</div><div class="t">${esc(e.title)}</div><div class="s">${esc(sub)}</div></button>${info}</div>`;
    }).join("") || `<p class="muted">${esc(t("Keine Titel."))} ${me && me.admin ? esc(t("Unter „Medienordner“ Ordner hinzufügen und einlesen.")) : ""}</p>`;
    $$("#grid .tile").forEach((t) => t.addEventListener("click", () => openDetail(t.dataset.kind, +t.dataset.id)));
    $$("#grid [data-info]").forEach((b) => b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const [k, id] = b.dataset.info.split(":"); const e = lib.find((x) => x.kind === k && x.id === +id);
      if (e) showPathPop(b, e.title, e.paths, k === "tvshow" ? t("Ordnerpfad") : (e.paths.length > 1 ? t("Dateipfade") : t("Dateipfad")));
    }));
    $$("#grid img").forEach((img) => img.addEventListener("error", () => { img.replaceWith(Object.assign(document.createElement("div"), { className: "ph", textContent: img.closest(".tile").querySelector(".t").textContent })); }));
  }

  // ---------- Dateipfade (nur DSM-Administratoren) ----------
  // Kopieren: Clipboard-API nur in sicherem Kontext (HTTPS); sonst execCommand.
  async function copyText(t) {
    try { if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(t); return true; } } catch (e) { /* Rückfall */ }
    const ta = Object.assign(document.createElement("textarea"), { value: t });
    ta.setAttribute("readonly", ""); ta.style.cssText = "position:fixed;top:0;left:0;opacity:0";
    document.body.appendChild(ta); ta.select(); ta.setSelectionRange(0, t.length);
    let ok = false; try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
    ta.remove(); return ok;
  }
  function wireCopy(root) {
    $$("[data-copy]", root).forEach((b) => b.addEventListener("click", async (ev) => {
      ev.preventDefault(); ev.stopPropagation();
      const ok = await copyText(b.dataset.copy);
      if (!ok) { const r = document.createRange(); r.selectNodeContents(b); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
      b.classList.remove("copied", "nocopy"); void b.offsetWidth; b.classList.add(ok ? "copied" : "nocopy");
      if (!b.classList.contains("pathcopy")) { b.dataset.label = b.dataset.label || b.textContent; b.textContent = ok ? t("✓ kopiert") : t("Kopieren nicht möglich"); }
      clearTimeout(b._t); b._t = setTimeout(() => { b.classList.remove("copied", "nocopy"); if (b.dataset.label) b.textContent = b.dataset.label; }, 1800);
    }));
  }
  function showPathPop(anchor, title, paths, label) {
    const pop = $("#pathPop");
    pop.innerHTML = `<div class="pp-head"><b>${esc(title)}</b><button type="button" class="pp-x" aria-label="${esc(t("Schließen"))}">✕</button></div>
      <div class="pp-l">${esc(t("{0} – anklicken zum Kopieren", label))}</div>${paths.map((p) => `<button type="button" class="pathcopy" data-copy="${esc(p)}">${esc(p)}</button>`).join("")}`;
    pop.hidden = false;
    const r = anchor.getBoundingClientRect(), w = pop.offsetWidth;
    const left = Math.max(8, Math.min(r.right - w, document.documentElement.clientWidth - w - 8));
    pop.style.left = left + window.scrollX + "px"; pop.style.top = r.bottom + window.scrollY + 6 + "px";
    $(".pp-x", pop).addEventListener("click", () => (pop.hidden = true));
    wireCopy(pop);
  }
  document.addEventListener("click", (ev) => { const pop = $("#pathPop"); if (!pop.hidden && !pop.contains(ev.target)) pop.hidden = true; });
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") $("#pathPop").hidden = true; });

  // Logo oben links: Klick → fließt groß in die Bildmitte, Klick → zurück in die Ecke.
  (() => {
    const small = $(".brand .partner");
    if (!small) return;
    const ov = document.createElement("div");
    ov.className = "logozoom"; ov.hidden = true;
    const big = document.createElement("img");
    big.className = "logobig"; big.alt = small.alt; big.draggable = false;
    ov.appendChild(big); document.body.appendChild(ov);
    const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");
    const ease = "cubic-bezier(.22,.8,.24,1)";
    let open = false, busy = false;
    // Transform, das das große Bild genau auf das kleine Logo legt
    const fromSmall = () => {
      const a = small.getBoundingClientRect(), b = big.getBoundingClientRect();
      const dx = a.left + a.width / 2 - (b.left + b.width / 2), dy = a.top + a.height / 2 - (b.top + b.height / 2);
      return `translate(${dx}px, ${dy}px) scale(${a.width / b.width})`;
    };
    const shade = (on) => ({ backgroundColor: on ? "rgba(8,10,14,.72)" : "rgba(8,10,14,0)", backdropFilter: on ? "blur(4px)" : "blur(0px)" });
    async function show() {
      if (open || busy) return;
      busy = true;
      if (!big.getAttribute("src")) {
        big.src = "logo-large.webp";
        try { await big.decode(); } catch (e) { big.src = small.src; }
      }
      ov.hidden = false;
      const dur = reduce.matches ? 1 : 520;
      const start = fromSmall();
      small.style.visibility = "hidden";
      ov.animate([shade(false), shade(true)], { duration: dur, easing: "ease-out", fill: "forwards" });
      await big.animate([{ transform: start }, { transform: "none" }], { duration: dur, easing: ease }).finished.catch(() => {});
      open = true; busy = false;
    }
    async function hide() {
      if (!open || busy) return;
      busy = true;
      const dur = reduce.matches ? 1 : 420;
      const end = fromSmall();
      ov.animate([shade(true), shade(false)], { duration: dur, easing: "ease-in", fill: "forwards" });
      await big.animate([{ transform: "none" }, { transform: end }], { duration: dur, easing: "cubic-bezier(.5,0,.75,.3)", fill: "forwards" }).finished.catch(() => {});
      ov.hidden = true;
      big.getAnimations().forEach((a) => a.cancel());
      ov.getAnimations().forEach((a) => a.cancel());
      small.style.visibility = "";
      open = false; busy = false;
    }
    small.addEventListener("click", show);
    small.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); show(); } });
    ov.addEventListener("click", hide);
    document.addEventListener("keydown", (e) => { if (e.key === "Escape" && open) hide(); });
  })();
  window.addEventListener("resize", () => ($("#pathPop").hidden = true));

  // File Station öffnen (neues Fenster) im Ordner der Datei. DSM läuft auf
  // demselben Rechner mit seinen Standardports (5000 HTTP / 5001 HTTPS);
  // wird die Oberfläche ohne Port aufgerufen (Reverse-Proxy), dieselbe Adresse.
  function dsmBase() {
    if (!location.port) return location.origin;
    return `${location.protocol}//${location.hostname}:${location.protocol === "https:" ? 5001 : 5000}`;
  }
  function openFileStation(path) {
    const dir = path.replace(/\/[^/]*$/, "") || "/";
    const share = dir.replace(/^\/volume[^/]+/, "") || "/"; // /volume1/video/Filme → /video/Filme
    const param = "openfile=" + encodeURIComponent(share.endsWith("/") ? share : share + "/");
    window.open(`${dsmBase()}/?launchApp=SYNO.SDS.App.FileStation3.Instance&launchParam=${encodeURIComponent(param)}`, "_blank", "noopener");
  }

  // ---------- Ordneransicht (nur DSM-Administratoren) ----------
  let tree = [], treeOpen = true;
  async function loadTree() {
    $("#treeMsg").textContent = t("Lade …");
    try { tree = await api("tree"); } catch (e) { $("#treeMsg").textContent = e.message; return; }
    renderTree();
  }
  const kindNames = { movie: t("Film"), episode: t("Serie"), home_video: t("Heimvideo"), tv_record: t("TV-Aufnahme") };
  const techLine = (f) => [f.width ? f.width + "×" + f.height : "", (f.vcodec || "").toUpperCase(), (f.acodec || "").toUpperCase(), f.duration ? fmtTime(f.duration) : "", fmtSize(f.size)].filter(Boolean).join(" · ");
  // Eine Datei (Ordner- und Doppelgänger-Ansicht)
  // beste Datei je Doppelgänger-Gruppe: Auflösung, dann Laufzeit, dann Größe
  const better = (a, b) => (a.width * a.height - b.width * b.height) || ((a.duration || 0) - (b.duration || 0)) || (a.size - b.size);
  function bestOf(g) { return g.reduce((k, f) => (better(f, k) > 0 ? f : k), g[0]); }
  // mode (nur Doppelgänger-Ansicht): "ign" = Knopf „Ignorieren“, "unign" = „Wieder anzeigen“
  function treeFileRow(f, withTech, keep, mode) {
    const open = f.kind === "episode" ? "tvshow:" + f.show_id : f.kind + ":" + f.id;
    const info = withTech ? techLine(f) : [f.sub, fmtSize(f.size)].filter(Boolean).join(" · ");
    const dup = !withTech && f.dups > 1 ? ` <span class="dupb" title="${esc(t("Diesen Titel gibt es {0}-mal", f.dups))}">${f.dups}×</span>` : keep ? ` <span class="keepb" title="${esc(t("Bleibt bei „Alle Doppelgänger löschen“ erhalten"))}">${esc(t("bleibt"))}</span>` : "";
    return `<div class="tfile">${f.poster ? `<img loading="lazy" src="${esc(f.poster)}" alt="">` : `<span class="noimg"></span>`}
      <div class="tf"><button type="button" class="tt" data-open="${open}">${esc(f.title)}</button> <span class="muted">${esc(info)}</span>${dup}
      <button type="button" class="pathcopy" data-fs="${esc(f.path)}" title="${esc(t("Ordner in der File Station öffnen (neues Fenster)"))}">${esc(f.path)}</button></div>
      <div class="tbtns">${mode === "ign" ? `<button type="button" class="secondary small" data-ign="${f.file_id}" title="${esc(t("Diese Datei künftig nicht mehr als Doppelgänger anzeigen"))}">${esc(t("Ignorieren"))}</button>`
        : mode === "unign" ? `<button type="button" class="secondary small" data-unign="${f.file_id}" title="${esc(t("Diese Datei wieder bei den Doppelgängern berücksichtigen"))}">${esc(t("Wieder anzeigen"))}</button>` : ""}
      <button type="button" class="danger small" data-del="${f.file_id}" title="${esc(t("Diese Datei löschen"))}">${esc(t("Löschen"))}</button></div></div>`;
  }
  function treeHit(f) {
    const q = $("#treeSearch").value.trim().toLowerCase(), k = $("#treeKind").value;
    return (!k || f.kind === k) && (!q || (f.title + " " + (f.sub || "") + " " + f.path).toLowerCase().includes(q));
  }
  function renderTree() {
    const dups = $("#treeView").value === "dups";
    $("#treeToggle").hidden = dups;
    dupShown = [];
    const filtered = !!($("#treeSearch").value.trim() || $("#treeKind").value);
    let html, msg = "";
    if (dups) {
      // Gruppen gleicher Titel über alle Medienordner
      const groups = new Map();
      tree.forEach((root) => root.dirs.forEach((d) => d.files.forEach((f) => {
        if (f.dups < 2) return;
        if (!groups.has(f.dup)) groups.set(f.dup, []);
        groups.get(f.dup).push({ ...f, root: root.library });
      })));
      const list = [...groups.values()].filter((g) => g.length > 1 && g.some(treeHit))
        .sort((a, b) => (a[0].title + (a[0].sub || "")).localeCompare(b[0].title + (b[0].sub || ""), "de"));
      const files = list.reduce((a, g) => a + g.length, 0);
      dupShown = list;
      msg = list.length ? t("{0} Titel mit mehreren Dateien ({1} Dateien)", list.length, files) : "";
      html = list.map((g) => { const k = bestOf(g); return `<div class="tdup"><div class="tdhead"><b>${esc(g[0].title)}</b> <span class="muted">${esc([kindNames[g[0].kind], g[0].sub].filter(Boolean).join(" · "))}</span><span class="dupb">${g.length}×</span></div>`
        + [k, ...g.filter((f) => f !== k).sort((a, b) => better(b, a))].map((f) => treeFileRow(f, true, f === k, "ign")).join("") + `</div>`; }).join("")
        || `<p class="muted">${esc(tree.length ? t("Keine Doppelgänger gefunden.") : t("Noch keine Medienordner."))}</p>`;
      // Ignorierte Dateien (rückgängig machen)
      const ign = [];
      tree.forEach((root) => root.dirs.forEach((d) => d.files.forEach((f) => { if (f.ignored && treeHit(f)) ign.push(f); })));
      if (ign.length) {
        ign.sort((a, b) => (a.title + (a.sub || "")).localeCompare(b.title + (b.sub || ""), "de"));
        html += `<details class="tdup tign"${ignOpen ? " open" : ""}><summary><b>${esc(t("Ignorierte Dateien"))}</b> <span class="muted">${esc(t("{0} – zählen nicht als Doppelgänger", ign.length))}</span></summary>`
          + ign.map((f) => treeFileRow(f, true, false, "unign")).join("") + `</details>`;
      }
    } else {
      let shown = 0;
      html = tree.map((root) => {
        const dirs = root.dirs.map((d) => ({ ...d, files: d.files.filter(treeHit) })).filter((d) => d.files.length);
        if (filtered && !dirs.length) return "";
        const n = dirs.reduce((a, d) => a + d.files.length, 0); shown += n;
        return `<div class="troot"><div class="trhead"><button type="button" class="pathcopy strong" data-copy="${esc(root.path)}">${esc(root.path)}</button><span class="badge">${esc(t(root.library))}</span><span class="muted">${esc(n === 1 ? t("1 Datei") : t("{0} Dateien", n))}</span></div>`
          + (dirs.map((d) => `<details class="tdir"${treeOpen || filtered ? " open" : ""}><summary><span class="dn">📁 ${esc(d.rel || t("(direkt im Medienordner)"))}</span><span class="muted">${d.files.length}</span><button type="button" class="link" data-copy="${esc(d.path)}">${esc(t("Ordnerpfad kopieren"))}</button></summary>`
            + d.files.map((f) => treeFileRow(f, false)).join("") + `</details>`).join("") || `<p class="muted">${esc(t("Keine Medien in diesem Ordner."))}</p>`)
          + `</div>`;
      }).join("") || `<p class="muted">${esc(tree.length ? t("Keine Treffer.") : t("Noch keine Medienordner."))}</p>`;
      msg = filtered ? t("{0} Treffer", shown) : "";
    }
    $("#treeBox").innerHTML = html;
    $("#treeMsg").textContent = msg;
    $("#dupDelAll").hidden = !dups || !dupShown.length;
    wireCopy($("#treeBox"));
    $$("#treeBox [data-fs]").forEach((b) => b.addEventListener("click", () => openFileStation(b.dataset.fs)));
    $$("#treeBox [data-open]").forEach((b) => b.addEventListener("click", () => { const [k, id] = b.dataset.open.split(":"); openDetail(k, +id); }));
    $$("#treeBox [data-del]").forEach((b) => b.addEventListener("click", () => deleteTreeFile(+b.dataset.del)));
    $$("#treeBox [data-ign]").forEach((b) => b.addEventListener("click", () => setDupIgnored(+b.dataset.ign, true, b)));
    $$("#treeBox [data-unign]").forEach((b) => b.addEventListener("click", () => setDupIgnored(+b.dataset.unign, false, b)));
    const ig = $("#treeBox .tign"); if (ig) ig.addEventListener("toggle", () => { ignOpen = ig.open; });
    $$("#treeBox img").forEach((img) => img.addEventListener("error", () => img.replaceWith(Object.assign(document.createElement("span"), { className: "noimg" }))));
  }

  // Datei löschen (Papierkorb der Freigabe, sonst endgültig; ohne Schreibrecht
  // des Pakets über DSM). ask: Schreibrecht-Rückfrage (je Freigabe einmal).
  async function deleteRequest(fid, ask) {
    const run = () => api("delete-file", { method: "POST", body: JSON.stringify({ file_id: fid }) });
    let r = await run();
    if (!r.ok && r.need_write) {
      if (!(await ask(r))) return r;
      $("#treeMsg").textContent = t("Setze Schreibrecht …");
      const g = await api("grant", { method: "POST", body: JSON.stringify({ path: r.path, write: true }) });
      if (!g.ok) return g;
      r = await run();
    }
    return r;
  }
  const askWrite = (r) => confirm(t(r.message) + "\n\n" + t("Schreibrecht für DS Video Revival auf „{0}“ jetzt in DSM setzen?", r.share));
  function dropFromTree(ids) {
    const gone = new Set(ids), keys = new Map();
    tree.forEach((root) => root.dirs.forEach((d) => d.files.forEach((x) => { if (gone.has(x.file_id) && !x.ignored) keys.set(x.dup, (keys.get(x.dup) || 0) + 1); })));
    tree.forEach((root) => {
      root.dirs.forEach((d) => {
        d.files = d.files.filter((x) => !gone.has(x.file_id));
        d.files.forEach((x) => { if (keys.has(x.dup) && !x.ignored) x.dups -= keys.get(x.dup); });
      });
      root.dirs = root.dirs.filter((d) => d.files.length);
    });
  }

  // Datei bei Doppelgängern ignorieren bzw. wieder berücksichtigen (dauerhaft, serverseitig).
  let ignOpen = false;
  async function setDupIgnored(fid, on, btn) {
    let f; tree.forEach((r) => r.dirs.forEach((d) => d.files.forEach((x) => { if (x.file_id === fid) f = x; })));
    if (!f) return;
    if (btn) btn.disabled = true;
    try {
      const r = await api("dup-ignore", { method: "POST", body: JSON.stringify({ file_id: fid, ignore: on }) });
      if (!r.ok) throw new Error(r.error || r.message || t("Fehler"));
    } catch (e) { $("#treeMsg").textContent = t("Fehler: {0}", t(e.message)); if (btn) btn.disabled = false; return; }
    // Zähler der Gruppe neu berechnen (ignorierte zählen nicht)
    f.ignored = on;
    const same = [];
    tree.forEach((r) => r.dirs.forEach((d) => d.files.forEach((x) => { if (x.dup === f.dup && !x.ignored) same.push(x); })));
    same.forEach((x) => { x.dups = same.length; });
    if (on) f.dups = 0;
    renderTree();
    $("#treeMsg").textContent = (on ? t("Wird künftig nicht mehr als Doppelgänger angezeigt: {0}", f.path) : t("Wieder bei den Doppelgängern berücksichtigt: {0}", f.path));
  }

  async function deleteTreeFile(fid) {
    let f; tree.forEach((r) => r.dirs.forEach((d) => d.files.forEach((x) => { if (x.file_id === fid) f = x; })));
    if (!f) return;
    const where = f.recycle ? t("Sie wird in den Papierkorb des freigegebenen Ordners verschoben.") : t("Sie wird ENDGÜLTIG gelöscht (kein Papierkorb aktiv). Das lässt sich nicht rückgängig machen.");
    if (!confirm(`${t("Diese Datei wirklich löschen?")}\n\n„${f.title}“${f.sub ? " (" + f.sub + ")" : ""}\n${f.path}\n${fmtSize(f.size)}\n\n${where}`)) return;
    try {
      const r = await deleteRequest(fid, askWrite);
      if (!r.ok) { $("#treeMsg").textContent = t(r.message); alert(t(r.message)); return; }
      dropFromTree([fid]);
      renderTree();
      $("#treeMsg").textContent = (r.via_dsm ? t("Über DSM gelöscht (wie in der File Station): {0}", f.path) : f.recycle ? t("In den Papierkorb verschoben: {0}", f.path) : t("Gelöscht: {0}", f.path));
    } catch (e) { $("#treeMsg").textContent = t("Löschen fehlgeschlagen: {0}", t(e.message)); }
  }

  // Alle angezeigten Doppelgänger löschen, je Titel bleibt die beste Datei.
  let dupShown = [];
  $("#dupDelAll").addEventListener("click", async () => {
    const victims = dupShown.flatMap((g) => { const k = bestOf(g); return g.filter((f) => f !== k); });
    if (!victims.length) return;
    const size = victims.reduce((a, f) => a + f.size, 0), final = victims.filter((f) => !f.recycle).length;
    if (!confirm(t("{0} Doppelgänger-Dateien aus {1} Titeln löschen (zusammen {2})?", victims.length, dupShown.length, fmtSize(size)) + "\n\n" + t("Je Titel bleibt die beste Datei erhalten (höchste Auflösung, dann längste Laufzeit, dann größte Datei) – in der Liste mit „bleibt“ markiert.")
      + "\n\n" + (final ? t("{0} Datei(en) liegen in Freigaben ohne aktiven Papierkorb und werden ENDGÜLTIG gelöscht.", final) : t("Alle Dateien kommen in den Papierkorb der jeweiligen Freigabe.")))) return;
    const btn = $("#dupDelAll"); btn.disabled = true;
    const asked = new Map(), done = [], errs = [];
    const ask = async (r) => { if (!asked.has(r.share)) asked.set(r.share, askWrite(r)); return asked.get(r.share); };
    for (const [i, f] of victims.entries()) {
      $("#treeMsg").textContent = t("Lösche {0} von {1} …", i + 1, victims.length);
      try {
        const r = await deleteRequest(f.file_id, ask);
        if (r.ok) done.push(f.file_id); else errs.push(`${f.path}: ${t(r.message)}`);
      } catch (e) { errs.push(`${f.path}: ${t(e.message)}`); }
    }
    btn.disabled = false;
    dropFromTree(done);
    renderTree();
    const msg = t("{0} von {1} Doppelgänger-Dateien gelöscht.", done.length, victims.length) + (errs.length ? " " + t("{0} nicht gelöscht.", errs.length) : "");
    $("#treeMsg").textContent = msg;
    if (errs.length) alert(msg + "\n\n" + errs.slice(0, 10).join("\n") + (errs.length > 10 ? "\n" + t("… und {0} weitere", errs.length - 10) : ""));
  });

  let treeT;
  $("#treeSearch").addEventListener("input", () => { clearTimeout(treeT); treeT = setTimeout(renderTree, 150); });
  $("#treeKind").addEventListener("change", renderTree);
  $("#treeView").addEventListener("change", renderTree);
  $("#treeToggle").addEventListener("click", () => {
    treeOpen = !treeOpen; $("#treeToggle").textContent = treeOpen ? t("Alle zuklappen") : t("Alle aufklappen");
    $$("#treeBox details").forEach((d) => (d.open = treeOpen));
  });

  // Aufnahmezeit „2023-11-05 20:15:00“ → „05.11.2023 20:15“
  function fmtRec(r) { const m = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2})/.exec(r || ""); return m ? `${m[3]}.${m[2]}.${m[1]} ${m[4]}:${m[5]}` : ""; }

  // ---------- Detail ----------
  async function openDetail(k, id) {
    const d = await api("item?kind=" + k + "&id=" + id + "&client=" + client);
    current = d;
    const m = d.meta && !d.meta.not_found ? d.meta : null;
    const hero = $("#dHero");
    hero.style.backgroundImage = d.backdrop ? `url("${d.backdrop}")` : "";
    hero.classList.toggle("empty", !d.backdrop);
    const p = $("#dPoster"); p.hidden = !d.poster; p.onerror = () => { p.hidden = true; };
    if (d.poster) p.src = d.poster; else p.removeAttribute("src");
    $("#dTitle").textContent = d.title;
    const facts = [];
    const year = d.year || (m && m.released ? m.released.slice(0, 4) : "");
    if (d.recorded) facts.push(`<span>${esc(t("Aufgenommen {0}", fmtRec(d.recorded)))}</span>`); else if (year) facts.push(`<span>${esc(year)}</span>`);
    if (d.channel) facts.push(`<span class="badge">${esc(d.channel)}</span>`);
    if (m && m.certificate) facts.push(`<span class="badge">${esc(m.certificate)}</span>`);
    if (m && m.runtime) facts.push(`<span>${Math.floor(m.runtime / 60)} h ${m.runtime % 60} min</span>`);
    if (m && m.genres && m.genres.length) facts.push(`<span>${esc(m.genres.join(", "))}</span>`);
    if (m && m.imdb_id) facts.push(`<a class="badge imdb" href="https://www.imdb.com/title/${esc(m.imdb_id)}/" target="_blank" rel="noopener">IMDb${m.imdb_rating ? " " + m.imdb_rating.toFixed(1) : ""}</a>`);
    if (m && m.rating) facts.push(`<span class="badge">${m.source === "tvmaze" || m.tvmaze_id ? "TVmaze" : "TMDb"} ${m.rating.toFixed(1)}</span>`);
    $("#dFacts").innerHTML = facts.join("");
    renderMyRating(d, m);
    $("#dTagline").textContent = (m && m.tagline) || "";
    $("#dSummary").textContent = (m && m.summary) || (m ? "" : t("Keine Metadaten gefunden."));
    const ppl = [];
    if (m && m.directors && m.directors.length) ppl.push(`<div><b>${esc(t("Regie:"))}</b> ${esc(m.directors.join(", "))}</div>`);
    if (m && m.writers && m.writers.length) ppl.push(`<div><b>${esc(t("Drehbuch:"))}</b> ${esc(m.writers.slice(0, 4).join(", "))}</div>`);
    if (m && m.actors && m.actors.length) ppl.push(`<div><b>${esc(t("Darsteller:"))}</b> ${esc(m.actors.slice(0, 8).join(", "))}</div>`);
    $("#dPeople").innerHTML = ppl.join("");
    $("#dEpisodes").innerHTML = ""; $("#dPlay").innerHTML = ""; $("#dFiles").innerHTML = ""; $("#dTech").textContent = "";
    if (d.kind === "tvshow") renderEpisodes(d); else renderFiles(d.files, d.title);
    $("#dAdmin").hidden = !(me && me.admin) || d.kind === "episode";
    if (me && me.admin) { renderFskSelect(d); renderTitleEdit(d); renderDelete(d); }
    $("#assignForm").reset(); // keine Eingabe vom zuvor geöffneten Titel übernehmen
    $("#assignMsg").textContent = m ? t("Aktuell: {0}", [m.imdb_id ? "IMDb " + m.imdb_id : "", m.tmdb_id ? "TMDb " + m.tmdb_id : "", t("Quelle {0}", m.source || "?")].filter(Boolean).join(", ") + (m.locked ? " " + t("(manuell)") : "")) : t("Kein Treffer – IMDb-ID (tt…) eintragen.");
    $("#detail").hidden = false;
    $("#dClose").focus();
  }

  // Titel von Hand ändern (gilt auch in den DS-video-Apps)
  function renderTitleEdit(d) {
    $("#dTitleIn").value = d.custom_title || d.title || "";
    $("#dTitleMsg").textContent = d.custom_title ? t("von Hand gesetzt") : t("automatisch (Dateiname bzw. Metadaten)");
    const save = async (title) => {
      try {
        const r = await api("title", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, title }) });
        d.title = r.title; d.custom_title = r.custom_title;
        $("#dTitle").textContent = r.title; renderTitleEdit(d); $("#dTitleMsg").textContent = t("Gespeichert.");
        const e = lib.find((x) => x.id === d.id && x.kind === d.kind); if (e) { e.title = r.title; renderGrid(); }
      } catch (e) { $("#dTitleMsg").textContent = t(e.message); }
    };
    $("#titleForm").onsubmit = (ev) => { ev.preventDefault(); save($("#dTitleIn").value.trim()); };
    $("#dTitleReset").onclick = () => save("");
  }

  // Titel löschen (nur Admin): Papierkorb der Freigabe, sonst endgültig.
  function renderDelete(d) {
    $("#dDelBox").hidden = !d.deletable;
    $("#dDelMsg").textContent = d.recycle ? t("Datei(en) kommen in den Papierkorb des freigegebenen Ordners.") : t("Kein Papierkorb aktiv – Datei(en) werden endgültig gelöscht.");
    $("#dDelete").onclick = async () => {
      const n = (d.files || []).length;
      const what = n > 1 ? t("{0} Dateien", n) : t("die Datei");
      if (!confirm(t("„{0}“ wirklich löschen?", d.title) + "\n\n" + (d.recycle ? t("Es wird {0} in den Papierkorb des freigegebenen Ordners verschoben.", what) : t("Es wird {0} ENDGÜLTIG gelöscht (kein Papierkorb aktiv). Das lässt sich nicht rückgängig machen.", what)))) return;
      const run = async () => api("delete", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id }) });
      try {
        let r = await run();
        if (!r.ok && r.need_write) {
          if (!askWrite(r)) { $("#dDelMsg").textContent = t(r.message); return; }
          $("#dDelMsg").textContent = t("Setze Schreibrecht …");
          const g = await api("grant", { method: "POST", body: JSON.stringify({ path: r.path, write: true }) });
          if (!g.ok) { $("#dDelMsg").textContent = t(g.message); return; }
          r = await run();
        }
        if (!r.ok) { $("#dDelMsg").textContent = t(r.message); return; }
        $("#detail").hidden = true;
        lib = lib.filter((x) => !(x.id === d.id && x.kind === d.kind)); renderGrid();
        $("#libMsg").textContent = t(r.message);
      } catch (e) { $("#dDelMsg").textContent = t(e.message); }
    };
  }

  // FSK von Hand setzen (Jugendschutz)
  function renderFskSelect(d) {
    const sel = $("#dFsk");
    const auto = d.fsk_manual ? t("automatisch (aus Metadaten)") : t("automatisch: {0}", d.fsk >= 0 ? "FSK " + d.fsk : t("unbekannt"));
    sel.innerHTML = `<option value="">${auto}</option>` + [0, 6, 12, 16, 18].map((n) => `<option value="${n}"${d.fsk_manual && d.fsk === n ? " selected" : ""}>FSK ${n}</option>`).join("");
    $("#dFskMsg").textContent = d.fsk_manual ? t("von Hand gesetzt") : "";
    sel.onchange = async () => {
      try {
        const r = await api("fsk", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, fsk: sel.value === "" ? null : +sel.value }) });
        d.fsk = r.fsk; d.fsk_manual = r.fsk_manual; renderFskSelect(d); $("#dFskMsg").textContent = t("Gespeichert.");
      } catch (e) { $("#dFskMsg").textContent = t(e.message); }
    };
  }

  // Eigene Bewertung 1–10 (gleiche Skala wie IMDb); IMDb dient als Empfehlung.
  function renderMyRating(d, m) {
    const v = d.my_rating || 0;
    const stars = Array.from({ length: 10 }, (_, i) => `<button type="button" data-r="${i + 1}" class="${i < Math.round(v) ? "on" : ""}" aria-label="${esc(t("{0} von 10", i + 1))}">★</button>`).join("");
    const rec = m && m.imdb_rating ? t("Empfehlung (IMDb): ★ {0}", m.imdb_rating.toFixed(1)) : (m && m.rating ? t("Empfehlung: ★ {0}", m.rating.toFixed(1)) : "");
    $("#dRate").innerHTML = `<span>${esc(t("Deine Bewertung:"))}</span><span class="stars" role="group" aria-label="${esc(t("Deine Bewertung – Stern anklicken"))}">${stars}</span><span class="val">${v ? (v % 1 ? v.toFixed(1) : v) + "/10" : esc(t("noch keine – Stern anklicken"))}</span>${v ? `<button type="button" class="link" data-r="0">${esc(t("entfernen"))}</button>` : ""}<span class="muted">${esc(rec)}</span><span class="err" id="rateErr"></span>`;
    const box = $("#dRate .stars");
    $$("#dRate .stars button").forEach((b) => {
      b.addEventListener("mouseenter", () => $$("#dRate .stars button").forEach((x) => x.classList.toggle("on", +x.dataset.r <= +b.dataset.r)));
    });
    box.addEventListener("mouseleave", () => $$("#dRate .stars button").forEach((x) => x.classList.toggle("on", +x.dataset.r <= Math.round(v))));
    $$("#dRate [data-r]").forEach((b) => b.addEventListener("click", async () => {
      const r = +b.dataset.r;
      try { await api("rate", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, rating: r }) }); }
      catch (e) { $("#rateErr").textContent = t("Bewertung nicht gespeichert: {0}", t(e.message)); return; }
      d.my_rating = r; renderMyRating(d, m);
      const e = lib.find((x) => x.id === d.id && x.kind === d.kind); if (e) { e.my_rating = r; renderGrid(); }
    }));
  }

  function renderFiles(files, title) {
    if (!files || !files.length) return;
    const f = files[0];
    const btn = [];
    if (f.position > 30) btn.push(`<button data-play="${f.id}" data-start="${f.position}">${esc(t("▶ Fortsetzen ab {0}", fmtTime(f.position)))}</button>`);
    btn.push(`<button data-play="${f.id}" data-start="0" ${f.position > 30 ? 'class="secondary"' : ""}>${esc(t("▶ Abspielen"))}</button>`);
    $("#dPlay").innerHTML = btn.join("");
    // Ergänzend unter der Beschreibung: Auflösung und Dateigröße
    $("#dTech").textContent = fileTech(files);
    // Keine Dateinamen/Speicherorte; bei mehreren Fassungen nur zur Auswahl.
    $("#dFiles").innerHTML = files.length < 2 ? "" : `<ul class="files">${files.map((x, i) => {
      const pr = x.probe || {};
      const tech = [pr.width ? pr.width + "×" + pr.height : "", fmtSize(x.size)].filter(Boolean).join(" · ");
      return `<li>${esc(t("Fassung {0}", i + 1))}${tech ? " · " + esc(tech) : ""} <button class="secondary" data-play="${x.id}" data-start="0">${esc(t("abspielen"))}</button></li>`;
    }).join("")}</ul>`;
    $$("[data-play]", $("#detail")).forEach((b) => b.addEventListener("click", () => play(+b.dataset.play, +b.dataset.start, title)));
  }

  // fileTech: „Auflösung: 1920×1080 (Full HD) · Dateigröße: 2.1 GB“ (erste Datei)
  function fileTech(files) {
    if (!files || !files.length) return "";
    const f = files[0], pr0 = f.probe || {};
    const tech = [];
    if (pr0.width) tech.push(t("Auflösung: {0}", `${pr0.width}×${pr0.height}${resLabel(pr0.width, pr0.height)}`));
    const total = files.reduce((a, x) => a + (x.size || 0), 0);
    tech.push(t("Dateigröße: {0}", fmtSize(f.size) + (files.length > 1 ? " " + t("({0} Dateien, zusammen {1})", files.length, fmtSize(total)) : "")));
    return tech.join(" · ");
  }
  function resLabel(w, h) {
    if (w >= 3800 || h >= 2100) return " (4K UHD)";
    if (w >= 1900 || h >= 1050) return " (Full HD)";
    if (w >= 1260 || h >= 700) return " (HD)";
    return w ? " (SD)" : "";
  }

  function renderEpisodes(d) {
    const seasons = {};
    (d.episodes || []).forEach((e) => (seasons[e.season] = seasons[e.season] || []).push(e));
    $("#dEpisodes").innerHTML = Object.keys(seasons).sort((a, b) => a - b).map((s) => `<div class="season"><h4>${esc(t("Staffel {0}", s))}</h4>${seasons[s].map((e) => {
      const f = (e.files || [])[0];
      return `<div class="ep">${e.still ? `<img src="${esc(e.still)}" alt="" loading="lazy">` : ""}<div class="eb"><div class="et">${e.episode}. ${esc(e.title || t("Folge {0}", e.episode))}</div>${e.summary ? `<p class="es">${esc(e.summary)}</p>` : ""}${f ? `<p class="etech">${esc(fileTech(e.files))}</p>` : ""}${f && f.position > 30 ? `<p>${esc(t("Gesehen bis {0}", fmtTime(f.position)))}</p>` : ""}</div>${f ? `<button data-play="${f.id}" data-start="${f.position > 30 ? f.position : 0}" data-title="${esc(d.title + " – S" + e.season + "E" + e.episode)}">▶</button>` : ""}</div>`;
    }).join("")}</div>`).join("") || `<p class="muted">${esc(t("Keine Folgen."))}</p>`;
    $$("[data-play]", $("#dEpisodes")).forEach((b) => b.addEventListener("click", () => play(+b.dataset.play, +b.dataset.start, b.dataset.title)));
  }

  $("#dClose").addEventListener("click", () => { $("#detail").hidden = true; });
  // Schließen per Klick neben das Fenster – nur wenn auch der Mausdruck dort
  // begann. Sonst schloss z.B. das Markieren von Text in einem Eingabefeld
  // das Fenster, wenn die Maus außerhalb losgelassen wurde.
  let downOnBackdrop = false;
  $("#detail").addEventListener("pointerdown", (e) => { downOnBackdrop = e.target.id === "detail"; });
  $("#detail").addEventListener("click", (e) => { if (e.target.id === "detail" && downOnBackdrop) $("#detail").hidden = true; downOnBackdrop = false; });
  $("#assignForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const ref = String(new FormData(ev.target).get("ref") || "").trim();
    $("#assignMsg").textContent = t("Wird geladen…");
    try { const r = await api("meta", { method: "POST", body: JSON.stringify({ action: "assign", kind: current.kind, id: current.id, ref }) }); $("#assignMsg").textContent = t("Zugeordnet: {0}", r.title); openDetail(current.kind, current.id); loadLibrary(); }
    catch (e) { $("#assignMsg").textContent = t("Fehler: {0}", t(e.message)); }
  });
  $("#dRefresh").addEventListener("click", async () => { await api("meta", { method: "POST", body: JSON.stringify({ action: "refresh", id: current.id }) }); $("#assignMsg").textContent = t("Wird neu geladen – in einigen Sekunden erneut öffnen."); });

  // ---------- Player ----------
  let hls = null, progT = null;
  async function play(fileId, startAt, title) {
    const v = $("#video");
    $("#pTitle").textContent = title || (current && current.title) || "";
    $("#pMsg").textContent = t("Wird vorbereitet…");
    $("#pMode").textContent = "";
    $("#player").hidden = false;
    let r;
    try { r = await api("play", { method: "POST", body: JSON.stringify({ file_id: fileId, client, start: startAt }) }); }
    catch (e) { $("#pMsg").textContent = t("Wiedergabe nicht möglich: {0}", t(e.message)); return; }
    playing = { token: r.token, fileId, offset: r.offset || 0 };
    $("#pMode").textContent = planLabel[r.mode] || r.mode;
    $("#pMsg").textContent = r.mode === "direct" ? "" : t(r.reason);
    const startPlayback = () => { v.play().catch(() => {}); };
    if (r.url.endsWith(".m3u8")) {
      attachHLS(v, r.url, startPlayback, r.seek || 0);
    } else {
      v.src = r.url;
      v.addEventListener("loadedmetadata", () => { if (startAt > 0) v.currentTime = startAt; startPlayback(); }, { once: true });
      v.addEventListener("error", () => { if (playing) $("#pMsg").innerHTML = `${esc(t("Direktwiedergabe fehlgeschlagen."))} <button id="forceTc" class="secondary">${esc(t("Umwandeln"))}</button>`; const b = $("#forceTc"); if (b) b.onclick = () => forceTranscode(fileId, startAt, title); }, { once: true });
    }
    clearInterval(progT);
    progT = setInterval(saveProgress, 15000);
  }

  async function forceTranscode(fileId, startAt, title) {
    await stopPlayback();
    $("#player").hidden = false;
    const r = await api("play", { method: "POST", body: JSON.stringify({ file_id: fileId, client, start: startAt, force: "transcode" }) }).catch((e) => ({ error: e.message }));
    if (r.error) { $("#pMsg").textContent = t(r.error); return; }
    playing = { token: r.token, fileId, offset: r.offset || 0 };
    $("#pMode").textContent = planLabel[r.mode];
    const v = $("#video");
    attachHLS(v, r.url, () => v.play().catch(() => {}), r.seek || 0);
  }

  // hls.js bevorzugen (MSE); nativ nur, wo MSE fehlt (iPhone). Die junge
  // native HLS-Unterstützung in Chromium scheitert an wachsenden fMP4-Listen.
  function attachHLS(v, url, onReady, seek) {
    if (window.Hls && window.Hls.isSupported()) {
      hls = new window.Hls({ maxBufferLength: 60, startPosition: seek > 0 ? seek : -1 });
      hls.on(window.Hls.Events.ERROR, (_, data) => { if (data.fatal) $("#pMsg").textContent = t("Wiedergabefehler: {0}", data.details); });
      hls.loadSource(url); hls.attachMedia(v);
      hls.on(window.Hls.Events.MANIFEST_PARSED, onReady);
    } else if (v.canPlayType("application/vnd.apple.mpegurl")) {
      v.src = url; v.addEventListener("loadedmetadata", () => { if (seek > 0) v.currentTime = seek; onReady(); }, { once: true });
    } else { $("#pMsg").textContent = t("Dieser Browser kann HLS nicht abspielen."); }
  }

  function saveProgress() {
    const v = $("#video");
    if (!playing || !v.currentTime) return;
    const pos = Math.floor(playing.offset + v.currentTime);
    api("progress", { method: "POST", body: JSON.stringify({ file_id: playing.fileId, position: pos }) }).catch(() => {});
  }

  async function stopPlayback() {
    saveProgress();
    clearInterval(progT);
    const v = $("#video");
    v.pause();
    if (hls) { hls.destroy(); hls = null; }
    v.removeAttribute("src"); v.load();
    if (playing) { const t = playing.token; playing = null; await api("play?token=" + encodeURIComponent(t), { method: "DELETE" }).catch(() => {}); }
  }
  $("#pClose").addEventListener("click", async () => { await stopPlayback(); $("#player").hidden = true; if (current) openDetail(current.kind, current.id); });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    if (!$("#picker").hidden) $("#picker").hidden = true; else if (!$("#player").hidden) $("#pClose").click(); else if (!$("#detail").hidden) $("#detail").hidden = true;
  });
  window.addEventListener("beforeunload", saveProgress);

  // ---------- Verwaltung ----------
  const kpi = (label, value) => `<div class="kpi"><b>${esc(value)}</b><span>${esc(label)}</span></div>`;
  async function loadStatus() {
    const s = await api("status");
    const lb = s.library || {}, items = lb.items || {}, md = s.metadata || {};
    const up = s.upstream_ok == null ? "–" : (s.upstream_ok ? t("erreichbar") : t("NICHT erreichbar"));
    $("#statusGrid").innerHTML = [
      kpi(t("Version"), s.version), kpi(t("Plattform"), s.arch), kpi(t("DSM-WebAPI"), up), kpi(t("Dateien"), lb.files ?? 0),
      kpi(t("Filme"), items.movie ?? 0), kpi(t("Serien / Folgen"), (lb.shows ?? 0) + " / " + (items.episode ?? 0)), kpi(t("Heimvideos"), items.home_video ?? 0), kpi(t("TV-Aufnahmen"), items.tv_record ?? 0),
      kpi(t("Metadaten ({0})", md.source === "tmdb" ? "TMDb" : "Wikidata/Wikipedia"), md.running ? t("läuft {0}/{1}", md.done, md.total) : t("{0} gefunden, {1} nicht", md.found, md.not_found)),
      kpi(t("IMDb-Bewertungen"), md.rating_error ? t("Fehler") : t("{0} aktualisiert", md.ratings ?? 0)),
      kpi("FFmpeg", s.ffmpeg ? t("vorhanden") : t("fehlt")),
      kpi(t("Umwandlung H.264"), !(s.transcode || {}).available ? t("nicht möglich") : (s.transcode.vaapi ? t("Hardware (VAAPI)") : s.transcode.h264) + (s.transcode.aac ? " + AAC" : " " + t("(kein AAC!)"))), kpi(t("Umwandlungen aktiv"), s.hls_jobs), kpi(t("Sitzungen"), s.sessions),
      kpi(t("Letzter Scan"), lb.last_scan && !lb.last_scan.startsWith("0001") ? new Date(lb.last_scan).toLocaleString(L) : (lb.scanning ? t("läuft…") : t("noch nie"))),
    ].join("");
    const errs = lb.scan_errors || [];
    $("#scanErrors").innerHTML = (md.last_error || md.rating_error ? `<h3>${esc(t("Metadaten"))}</h3><p class="err">${esc([md.last_error, md.rating_error].filter(Boolean).join(" · "))}</p>` : "") + (errs.length ? `<h3>${esc(t("Hinweise beim letzten Scan"))}</h3><ul>` + errs.slice(0, 50).map((e) => `<li>${esc(e)}</li>`).join("") + "</ul>" : "");
  }
  $("#scanBtn").addEventListener("click", async () => { const r = await api("scan", { method: "POST" }); $("#scanBtn").textContent = t(r.message); setTimeout(() => { $("#scanBtn").textContent = t("Bibliothek jetzt einlesen"); loadStatus(); }, 3000); });

  const typeLabel = { movie: t("Filme"), tvshow: t("Serien"), home_video: t("Heimvideos"), tv_record: t("TV-Aufnahmen") };
  // Auswahl „Bibliothek“ für Medienordner: Standardbibliotheken + eigene (Wert typ:id)
  function libOptions(sel) {
    return stdLibs.map(([t, l]) => `<option value="${t}:0"${sel === t + ":0" ? " selected" : ""}>${l}</option>`).join("")
      + ((cfg && cfg.libraries) || []).map((l) => `<option value="${esc(l.type)}:${l.id}"${sel === l.type + ":" + l.id ? " selected" : ""}>${esc(l.title)} (${typeLabel[l.type]})</option>`).join("");
  }
  const libName = (t, id) => (id ? (((cfg && cfg.libraries) || []).find((l) => l.id === id) || {}).title || "?" : typeLabel[t]);
  // ---------- Benutzerrechte ----------
  let acc = null;
  let gEdit = null; // {old, name, members}
  const whoLabel = (k) => (k.startsWith("vgroup:") ? t("Gruppe") + " " : k.startsWith("group:") ? t("DSM-Gruppe") + " " : "") + k.replace(/^(user|group|vgroup):/, "");
  async function loadAccess() {
    acc = await api("access");
    gEdit = null;
    renderAccess();
  }
  // Mehrfachauswahl als Aufklappmenü
  function multiDrop(el, sections, selected, emptyLabel, onChange) {
    const sum = () => (selected.length ? selected.map(whoLabel).join(", ") : emptyLabel);
    el.classList.add("mdrop");
    el.innerHTML = `<button type="button" class="secondary" aria-haspopup="true" aria-expanded="false"><span>${esc(sum())}</span><span aria-hidden="true">▾</span></button>
      <div class="panel" hidden>${sections.filter((x) => x.items.length).map((x) => `<h5>${esc(x.title)}</h5>` + x.items.map((k) => `<label><input type="checkbox" value="${esc(k)}"${selected.includes(k) ? " checked" : ""}>${esc(k.replace(/^(user|group|vgroup):/, ""))}</label>`).join("")).join("") || `<p class="muted" style="padding:6px 12px">${esc(t("Keine Einträge."))}</p>`}</div>`;
    const btn = el.querySelector("button"), panel = el.querySelector(".panel");
    btn.addEventListener("click", (e) => { e.stopPropagation(); const open = panel.hidden; $$(".mdrop .panel").forEach((p) => (p.hidden = true)); panel.hidden = !open; btn.setAttribute("aria-expanded", String(open)); });
    panel.addEventListener("click", (e) => e.stopPropagation());
    panel.querySelectorAll("input").forEach((c) => c.addEventListener("change", () => {
      if (c.checked) selected.push(c.value); else selected.splice(selected.indexOf(c.value), 1);
      btn.querySelector("span").textContent = sum();
      onChange && onChange(selected);
    }));
  }
  document.addEventListener("click", () => $$(".mdrop .panel").forEach((p) => (p.hidden = true)));

  function renderAccess() {
    const users = acc.users.map((u) => "user:" + u.toLowerCase());
    const dsmGroups = acc.groups.map((g) => "group:" + g.toLowerCase());
    const AG = acc.admin_group || "administrators";
    // Administratorengruppe nicht in Freigaben/Altersgrenzen anbieten (Admins sehen immer alles)
    const vgroups = Object.keys(acc.vgroups).filter((g) => g !== AG).sort().map((g) => "vgroup:" + g);
    const known = [...vgroups, ...dsmGroups, ...users];
    acc.download_allow = acc.download_allow || [];
    acc.single_session = acc.single_session || [];
    const extra = [...acc.folders.flatMap((f) => f.allow), ...Object.keys(acc.restrictions), ...acc.download_allow, ...acc.single_session].filter((k) => !known.includes(k) && k !== "vgroup:" + AG);
    const sections = [{ title: t("Gruppen"), items: vgroups }, { title: t("DSM-Gruppen"), items: dsmGroups }, { title: tc("liste", "Benutzer"), items: [...users, ...extra.filter((k) => k.startsWith("user:"))] }];

    // Gruppen: Anlegen, Bearbeiten (Name, Mitglieder) und Löschen werden sofort gespeichert.
    const gl = Object.keys(acc.vgroups).filter((g) => g !== AG).sort();
    const gForm = () => `<div class="gedit">${gEdit.old === AG ? "" : `<label>${esc(t("Name"))} <input type="text" id="gName" value="${esc(gEdit.name)}" maxlength="64"></label>`}<span id="gMembers"></span>
        <button type="button" id="gOk">${esc(gEdit.old ? t("Speichern") : t("Anlegen"))}</button><button type="button" id="gCancel" class="secondary">${esc(t("Abbrechen"))}</button><span id="gMsg" class="err"></span></div>`;
    // Administratoren (eigener Abschnitt, nicht löschbar)
    $("#accAdmins").innerHTML = gEdit && gEdit.old === AG ? gForm()
      : `<div class="accrow"><div><span class="members">${esc((acc.vgroups[AG] || []).join(", ") || t("keine weiteren Mitglieder"))} · ${esc(t("dazu immer die DSM-Administratoren"))}</span></div>
        <div class="row"><button type="button" class="secondary small" data-gedit="${esc(AG)}">${esc(t("Mitglieder bearbeiten"))}</button></div></div>`;
    // Eigene Gruppen
    $("#accGroups").innerHTML = gl.map((g) => gEdit && gEdit.old === g ? gForm()
      : `<div class="accrow"><div><b>${esc(g)}</b> <span class="members">${esc((acc.vgroups[g] || []).join(", ") || t("keine Mitglieder"))}</span></div>
        <div class="row"><button type="button" class="secondary small" data-gedit="${esc(g)}">${esc(t("Bearbeiten"))}</button><button type="button" class="secondary small" data-gdel="${esc(g)}">${esc(t("Löschen"))}</button></div></div>`).join("")
      + (gEdit && !gEdit.old ? gForm() : "")
      || `<p class="muted">${esc(t("Noch keine eigenen Gruppen."))}</p>`;
    $("#accNewGroup").hidden = !!gEdit;
    if (gEdit) {
      multiDrop($("#gMembers"), [{ title: tc("liste", "Benutzer"), items: users }], gEdit.members, t("Mitglieder wählen"));
      $("#gOk").addEventListener("click", async () => {
        const name = gEdit.old === AG ? AG : $("#gName").value.trim().toLowerCase();
        if (!name || /[:\n]/.test(name)) { $("#gMsg").textContent = t("Bitte einen Namen ohne „:“ eingeben."); return; }
        if (name !== gEdit.old && acc.vgroups[name]) { $("#gMsg").textContent = t("Gruppe gibt es schon."); return; }
        const members = gEdit.members.map((k) => k.replace(/^user:/, ""));
        const was = gEdit.old;
        if (was && was !== name) { renamePrincipal("vgroup:" + was, "vgroup:" + name); delete acc.vgroups[was]; }
        acc.vgroups[name] = members; gEdit = null;
        if (was === AG) await saveAccess(t("Administratoren gespeichert."), "#accAdminMsg");
        else await saveAccess(was ? (was !== name ? t("Gruppe „{0}“ in „{1}“ umbenannt und gespeichert.", was, name) : t("Gruppe „{0}“ gespeichert.", name)) : t("Gruppe „{0}“ angelegt.", name));
      });
      $("#gCancel").addEventListener("click", () => { gEdit = null; renderAccess(); });
      if ($("#gName")) $("#gName").focus();
    }
    $$("#accGroups [data-gedit], #accAdmins [data-gedit]").forEach((b) => b.addEventListener("click", () => {
      const g = b.dataset.gedit; gEdit = { old: g, name: g, members: (acc.vgroups[g] || []).map((u) => "user:" + u.toLowerCase()) }; renderAccess();
    }));
    $$("#accGroups [data-gdel]").forEach((b) => b.addEventListener("click", async () => {
      const g = b.dataset.gdel;
      if (g === AG) return;
      if (!window.confirm(t("Gruppe „{0}“ löschen? Ordnerfreigaben und Altersgrenzen dieser Gruppe entfallen.", g))) return;
      delete acc.vgroups[g]; renamePrincipal("vgroup:" + g, null); gEdit = null;
      await saveAccess(t("Gruppe „{0}“ gelöscht.", g));
    }));

    // Ordnerfreigaben
    $("#accFolders").innerHTML = acc.folders.map((f, i) => `<div class="accrow"><code>${esc(f.path)}</code><div data-fd="${i}"></div></div>`).join("") || `<p class="muted">${esc(t("Noch keine Medienordner."))}</p>`;
    const unsaved = () => { $("#accMsg").textContent = t("Noch nicht gespeichert."); };
    acc.folders.forEach((f, i) => multiDrop($(`[data-fd="${i}"]`), sections, f.allow, t("Alle"), unsaved));

    // Herunterladen (Download und Offline-Umwandlung in den Apps)
    multiDrop($("#accDownload"), sections, acc.download_allow, t("Alle"), unsaved);
    // Nur eine Verbindung gleichzeitig
    multiDrop($("#accSingle"), sections, acc.single_session, t("Niemand"), unsaved);
    $("#accIdle").value = acc.single_session_idle || 5;

    // Jugendschutz
    const keys = Object.keys(acc.restrictions).sort();
    $("#accRules").innerHTML = keys.map((k) => {
      const r = acc.restrictions[k];
      return `<tr><td>${esc(whoLabel(k))}</td><td><select data-max="${esc(k)}">${acc.levels.map((l) => `<option value="${l}"${r.max_fsk === l ? " selected" : ""}>FSK ${l}</option>`).join("")}</select></td>
        <td><label class="check"><input type="checkbox" data-unr="${esc(k)}"${r.block_unrated ? " checked" : ""}> ${esc(t("sperren"))}</label></td>
        <td><button type="button" class="secondary" data-delr="${esc(k)}">${esc(t("Entfernen"))}</button></td></tr>`;
    }).join("") || `<tr><td colspan="4" class="muted">${esc(t("Keine Altersgrenzen – alle sehen alle Titel ihrer Ordner."))}</td></tr>`;
    $$("#accRules [data-max]").forEach((s) => s.addEventListener("change", () => { acc.restrictions[s.dataset.max].max_fsk = +s.value; }));
    $$("#accRules [data-unr]").forEach((c) => c.addEventListener("change", () => { acc.restrictions[c.dataset.unr].block_unrated = c.checked; }));
    $$("#accRules [data-delr]").forEach((b) => b.addEventListener("click", () => { delete acc.restrictions[b.dataset.delr]; renderAccess(); }));
    $("#accNewWho").innerHTML = sections.map((x) => x.items.filter((k) => !acc.restrictions[k]).length ? `<optgroup label="${esc(x.title)}">` + x.items.filter((k) => !acc.restrictions[k]).map((k) => `<option value="${esc(k)}">${esc(k.replace(/^(user|group|vgroup):/, ""))}</option>`).join("") + "</optgroup>" : "").join("");
    $("#accAdd").disabled = !$("#accNewWho").options.length;
  }
  // Gruppe umbenennen/löschen: Verweise in Freigaben und Regeln nachziehen
  function renamePrincipal(from, to) {
    acc.folders.forEach((f) => { const i = f.allow.indexOf(from); if (i >= 0) { if (to) f.allow[i] = to; else f.allow.splice(i, 1); } });
    if (acc.restrictions[from]) { if (to) acc.restrictions[to] = acc.restrictions[from]; delete acc.restrictions[from]; }
    for (const list of [acc.download_allow || [], acc.single_session || []]) {
      const d = list.indexOf(from);
      if (d >= 0) { if (to) list[d] = to; else list.splice(d, 1); }
    }
  }
  $("#accNewGroup").addEventListener("click", () => { gEdit = { old: "", name: "", members: [] }; renderAccess(); });
  $("#accAdd").addEventListener("click", () => {
    const k = $("#accNewWho").value; if (!k) return;
    acc.restrictions[k] = { max_fsk: 12, block_unrated: true }; renderAccess();
  });
  // Benutzerrechte speichern (Gruppen, Freigaben, Altersgrenzen)
  async function saveAccess(okMsg, target = "#accGroupMsg") {
    const allow = {}; acc.folders.forEach((f) => { allow[f.path] = f.allow; });
    try {
      await api("access", { method: "PUT", body: JSON.stringify({ allow, restrictions: acc.restrictions, groups: acc.vgroups, download_allow: acc.download_allow || [], single_session: acc.single_session || [], single_session_idle: Math.round(+$("#accIdle").value) || 5 }) });
      await loadAccess();
      $("#accMsg").textContent = t("Gespeichert."); $("#accGroupMsg").textContent = ""; $("#accAdminMsg").textContent = ""; $(target).textContent = okMsg || "";
    } catch (e) {
      $("#accMsg").textContent = t("Fehler: {0}", t(e.message)); $(target).textContent = t("Fehler: {0}", t(e.message));
      await loadAccess(); // verworfene Änderung nicht stehen lassen
    }
  }
  $("#accSave").addEventListener("click", () => saveAccess(""));

  // ---------- Verbindungen ----------
  const connState = { play: t("spielt ab"), active: t("aktiv"), idle: t("ruhend"), blocked: t("gesperrt (2. Verbindung)"), banned: t("gesperrt") };
  try { const b = localStorage.getItem("dsvr_connBlock"); if (b !== null) $("#connBlock").value = b; } catch (e) { /* kein Speicher */ }
  $("#connBlock").addEventListener("change", () => { try { localStorage.setItem("dsvr_connBlock", $("#connBlock").value); } catch (e) {} });
  const blockLabel = () => $("#connBlock").selectedOptions[0].textContent;
  const connTime = (t) => { const d = new Date(t); if (isNaN(d)) return ""; const today = new Date().toDateString() === d.toDateString();
    return (today ? "" : d.toLocaleDateString(L, { day: "2-digit", month: "2-digit" }) + " ") + d.toLocaleTimeString(L, { hour: "2-digit", minute: "2-digit" }); };
  const ago = (x) => { const s = Math.max(0, Math.round((Date.now() - new Date(x)) / 1000)); return s < 60 ? t("gerade eben") : s < 3600 ? t("vor {0} Min.", Math.round(s / 60)) : s < 172800 ? t("vor {0} Std.", Math.floor(s / 3600)) : t("vor {0} Tagen", Math.floor(s / 86400)); };
  async function loadConns() {
    let r;
    try { r = await api("connections"); } catch (e) { $("#connMsg").textContent = t("Fehler: {0}", t(e.message)); return; }
    const list = r.connections || [];
    $("#connRows").innerHTML = list.map((c) => {
      const self = c.id === r.self;
      const kind = c.kind === "web" ? t("Weboberfläche") : c.kind === "jellyfin" ? t("Jellyfin-App") : t("App");
      return `<tr><td>${esc(c.account)}${c.single ? `<span class="one" title="${esc(t("Nur eine Verbindung gleichzeitig"))}">${esc(t("nur 1"))}</span>` : ""}</td>
        <td>${esc(c.device || kind)}<span class="sub">${esc(kind)}${c.ip ? " · " + esc(c.ip) : ""}</span></td>
        <td><span class="cst ${esc(c.state)}">${esc(c.state === "banned" ? t("gesperrt bis {0}", connTime(c.until)) : connState[c.state] || c.state)}</span></td>
        <td data-l="${esc(t("Titel"))}">${esc(c.title || "–")}</td>
        <td data-l="${esc(t("Verbunden seit"))}">${esc(connTime(c.started))}</td>
        <td data-l="${esc(t("Letzte Aktivität"))}">${esc(ago(c.last_seen))}</td>
        <td>${self ? `<span class="muted">${esc(t("diese Sitzung"))}</span>` : `<button type="button" class="danger small" data-kick="${esc(c.id)}" data-who="${esc(c.account + " – " + (c.device || kind))}">${esc(t("Trennen"))}</button>`}</td></tr>`;
    }).join("") || `<tr><td colspan="7" class="muted">${esc(t("Keine aktiven Verbindungen."))}</td></tr>`;
    $$("#connRows [data-kick]").forEach((b) => b.addEventListener("click", async () => {
      const block = +$("#connBlock").value;
      if (!window.confirm(block ? t("Verbindung „{0}“ trennen und das Gerät {1} sperren? Eine laufende Wiedergabe bricht ab.", b.dataset.who, blockLabel()) : t("Verbindung „{0}“ trennen? Eine laufende Wiedergabe bricht ab.", b.dataset.who))) return;
      b.disabled = true;
      try { await api("connections", { method: "POST", body: JSON.stringify({ id: b.dataset.kick, block_minutes: block }) }); $("#connMsg").textContent = block ? t("„{0}“ getrennt, {1} gesperrt.", b.dataset.who, blockLabel()) : t("„{0}“ getrennt.", b.dataset.who); }
      catch (e) { $("#connMsg").textContent = t("Fehler: {0}", t(e.message)); }
      loadConns();
    }));
    const bans = r.bans || [];
    $("#banBox").hidden = !bans.length;
    $("#banRows").innerHTML = bans.map((x) => `<tr><td>${esc(x.account)}</td><td>${esc(x.device || "–")}<span class="sub">${esc(x.ip || "")}</span></td>
      <td data-l="${esc(t("Gesperrt bis"))}">${esc(connTime(x.until))}</td><td><button type="button" class="secondary small" data-unban="${esc(x.id)}">${esc(t("Sperre aufheben"))}</button></td></tr>`).join("");
    $$("#banRows [data-unban]").forEach((b) => b.addEventListener("click", async () => {
      b.disabled = true;
      try { await api("connections", { method: "POST", body: JSON.stringify({ unban: b.dataset.unban }) }); $("#connMsg").textContent = t("Sperre aufgehoben."); }
      catch (e) { $("#connMsg").textContent = t("Fehler: {0}", t(e.message)); }
      loadConns();
    }));
  }
  $("#connReload").addEventListener("click", () => { $("#connMsg").textContent = ""; loadConns(); });
  setInterval(() => { if (!$('[data-panel="conns"]').hidden) loadConns().catch(() => {}); }, 10000);

  async function loadFolders() {
    cfg = await api("config");
    const st = await api("status");
    const readable = {}; (st.folders || []).forEach((f) => (readable[f.path] = f));
    $("#folderRows").innerHTML = (cfg.folders || []).map((f, i) => {
      const r = readable[f.path], ok = r && r.readable;
      return `<tr><td><code>${esc(f.path)}</code></td><td><select data-type="${i}" aria-label="${esc(t("Bibliothek"))}">${libOptions(f.type + ":" + (f.library_id || 0))}</select></td><td class="${ok ? "yes" : "no"}">${ok ? esc(t("ja")) : esc(t("nein")) + (r && r.error ? " – " + esc(r.error) : "")}</td><td><button class="secondary" data-del="${i}">${esc(t("Entfernen"))}</button></td></tr>`;
    }).join("") || `<tr><td colspan="4" class="muted">${esc(t("Noch keine Ordner."))}</td></tr>`;
    $$("[data-del]").forEach((b) => b.addEventListener("click", () => { cfg.folders.splice(+b.dataset.del, 1); saveCfg(); }));
    $$("[data-type]").forEach((s) => s.addEventListener("change", () => {
      const f = cfg.folders[+s.dataset.type];
      const [ty, id] = s.value.split(":");
      if (!window.confirm(t("{0} der Bibliothek „{1}“ zuordnen? Die Titel darin werden neu eingeordnet (gespeicherte Wiedergabepositionen dieser Dateien gehen dabei verloren).", f.path, libName(ty, +id)))) { s.value = f.type + ":" + (f.library_id || 0); return; }
      f.type = ty; f.library_id = +id; saveCfg();
    }));
    $("#scanInt").value = cfg.scan_interval_minutes;
    $("#pkType").innerHTML = libOptions("movie:0");
    renderLibs();
  }
  // Eigene Bibliotheken anlegen, umbenennen, löschen
  function renderLibs() {
    const libs = cfg.libraries || [];
    $("#libRows").innerHTML = libs.map((l, i) => {
      const n = (cfg.folders || []).filter((f) => f.library_id === l.id).length;
      return `<div class="accrow"><div class="row wrap"><input data-lname="${i}" value="${esc(l.title)}" maxlength="64" aria-label="${esc(t("Name"))}"><span class="muted">${esc(typeLabel[l.type])} · ${esc(t("{0} Ordner", n))}</span></div>
        <div class="row"><button type="button" class="secondary small" data-lsave="${i}">${esc(t("Umbenennen"))}</button><button type="button" class="secondary small" data-ldel="${i}">${esc(t("Löschen"))}</button></div></div>`;
    }).join("") || `<p class="muted">${esc(t("Noch keine eigenen Bibliotheken."))}</p>`;
    $$("[data-lsave]").forEach((b) => b.addEventListener("click", () => {
      const l = libs[+b.dataset.lsave], name = $(`[data-lname="${b.dataset.lsave}"]`).value.trim();
      if (!name) { $("#libMsgCfg").textContent = t("Bitte einen Namen eingeben."); return; }
      l.title = name; saveCfg();
    }));
    $$("[data-ldel]").forEach((b) => b.addEventListener("click", () => {
      const l = libs[+b.dataset.ldel];
      const fs = (cfg.folders || []).filter((f) => f.library_id === l.id);
      if (!window.confirm(t("Bibliothek „{0}“ löschen?", l.title) + (fs.length ? "\n\n" + t("Ihre {0} Ordner kommen in die Standardbibliothek „{1}“ (Dateien bleiben erhalten).", fs.length, typeLabel[l.type]) : ""))) return;
      fs.forEach((f) => (f.library_id = 0));
      cfg.libraries = libs.filter((x) => x !== l); saveCfg();
    }));
  }
  $("#libNew").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const name = $("#libNewName").value.trim();
    if (!name) { $("#libMsgCfg").textContent = t("Bitte einen Namen eingeben."); return; }
    cfg.libraries = cfg.libraries || [];
    if (cfg.libraries.some((l) => l.title.toLowerCase() === name.toLowerCase())) { $("#libMsgCfg").textContent = t("Diese Bibliothek gibt es schon."); return; }
    const id = cfg.libraries.reduce((m, l) => Math.max(m, l.id), 0) + 1;
    cfg.libraries.push({ id, title: name, type: $("#libNewType").value, is_public: true });
    $("#libNewName").value = ""; $("#libMsgCfg").textContent = "";
    saveCfg();
  });
  async function saveCfg() {
    cfg.scan_interval_minutes = parseInt($("#scanInt").value || "0", 10);
    try { await api("config", { method: "PUT", body: JSON.stringify(cfg) }); $("#cfgMsg").textContent = t("Gespeichert, Bibliothek wird neu eingelesen."); }
    catch (e) { $("#cfgMsg").textContent = t("Fehler: {0}", t(e.message)); }
    try { me = await api("me"); buildKindTabs(); } catch (e) {}
    loadFolders();
  }
  $("#saveCfg").addEventListener("click", saveCfg);
  // Ordnerauswahl: freigegebene Ordner durchblättern, Leserecht für den Paketbenutzer setzen.
  let pkCur = "";
  async function pkLoad(path) {
    $("#pkMsg").textContent = ""; $("#pkInfo").textContent = t("Lade …"); $("#pkList").innerHTML = "";
    let r;
    try { r = await api("browse?path=" + encodeURIComponent(path)); }
    catch (e) {
      if (!path) { $("#pkInfo").textContent = ""; $("#pkMsg").textContent = t(e.message); return; }
      // Unterordner nicht auflistbar (noch kein Recht): Ordner selbst bleibt wählbar.
      const up = path.replace(/\/[^/]+$/, "");
      r = { dirs: [], parent: /^\/volume[^/]+$/.test(up) ? "" : up, readable: false, user: "dsvideorevival", note: t("Unterordner werden nach dem Setzen des Leserechts sichtbar.") };
    }
    pkCur = path;
    $("#pkPath").textContent = path || t("Freigegebene Ordner");
    $("#pkUp").disabled = !path; $("#pkUp").dataset.to = r.parent || "";
    $("#pkAdd").disabled = !path;
    $("#pkList").innerHTML = (r.dirs || []).map((d) => `<li><button type="button" data-to="${esc(d.path)}"><span>📁 ${esc(d.name)}</span></button></li>`).join("")
      || `<li class="muted" style="padding:9px 12px">${esc(t("Keine Unterordner."))}</li>`;
    $$("#pkList [data-to]").forEach((b) => b.addEventListener("click", () => pkLoad(b.dataset.to)));
    $("#pkInfo").textContent = !path ? t("Ordner antippen, um ihn zu öffnen.") :
      (r.readable ? "" : t("Das Leserecht für DS Video Revival wird beim Hinzufügen automatisch gesetzt.") + (r.note ? " " + t(r.note) : ""));
  }
  $("#addFolder").addEventListener("click", () => { $("#picker").hidden = false; pkLoad(""); $("#pkClose").focus(); });
  $("#pkClose").addEventListener("click", () => { $("#picker").hidden = true; });
  $("#pkUp").addEventListener("click", () => pkLoad($("#pkUp").dataset.to || ""));
  $("#pkAdd").addEventListener("click", async () => {
    const path = pkCur; if (!path) return;
    if ((cfg.folders || []).some((f) => f.path === path)) { $("#pkMsg").textContent = t("Dieser Ordner ist bereits eingetragen."); return; }
    $("#pkAdd").disabled = true; $("#pkMsg").textContent = "";
    let g;
    try { g = await api("grant", { method: "POST", body: JSON.stringify({ path }) }); }
    catch (e) { g = { ok: false, message: e.message }; }
    $("#pkAdd").disabled = false;
    if (!g.ok) { $("#pkMsg").textContent = t(g.message); return; }
    const [t, lid] = $("#pkType").value.split(":");
    cfg.folders = cfg.folders || []; cfg.folders.push({ path, type: t, library_id: +lid });
    $("#picker").hidden = true;
    $("#folderMsg").textContent = t(g.message);
    saveCfg();
  });

  async function loadSettings() {
    const s = await api("settings");
    const f = $("#setForm");
    for (const [k, v] of Object.entries(s)) { const el = f.elements[k]; if (!el) continue; if (el.type === "checkbox") el.checked = !!v; else el.value = v ?? ""; }
    $("#restartMsg").textContent = s.running_web_port && s.web_port && s.running_web_port !== s.web_port
      ? t("Neuer Port {0} wird nach dem Neustart wirksam (läuft noch auf {1}).", s.web_port, s.running_web_port) : "";
    const st = await api("status"); const md = st.metadata || {};
    $("#metaState").textContent = md.enabled ? (md.running ? t("läuft {0}/{1}", md.done, md.total) : t("{0} gefunden, {1} nicht gefunden", md.found, md.not_found)) + (md.last_error ? " – " + md.last_error : "") : "";
  }
  $("#setForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = ev.target, body = {};
    for (const el of f.elements) { if (!el.name) continue; body[el.name] = el.type === "checkbox" ? el.checked : (el.type === "number" ? parseInt(el.value || "0", 10) : el.value); }
    try {
      const r = await api("settings", { method: "PUT", body: JSON.stringify(body) });
      $("#setMsg").textContent = t(r.message);
      $("#restartMsg").textContent = r.restart ? t("Neustart nötig, damit die Änderung wirkt.") : "";
      $("#restartBtn").classList.toggle("secondary", !r.restart);
      me = await api("me"); applyMe();
    } catch (e) { $("#setMsg").textContent = t("Fehler: {0}", t(e.message)); }
  });

  // Neustart: Prozess startet neu (gleiche PID); bei geändertem Port danach dorthin wechseln.
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  $("#restartBtn").addEventListener("click", async () => {
    if (!window.confirm(t("DS Video Revival jetzt neu starten? Laufende Wiedergaben brechen ab."))) return;
    const btn = $("#restartBtn"), msg = (x) => { $("#restartMsg").textContent = x; };
    let s = null, before = 0;
    try { s = await api("settings"); } catch (e) { /* ohne Portwechsel */ }
    try { before = (await (await fetch("/dsvr/api/health", { cache: "no-store" })).json()).started || 0; } catch (e) { /* egal */ }
    try { await api("restart", { method: "POST" }); } catch (e) { msg(t("Fehler: {0}", t(e.message))); return; }
    btn.disabled = true;
    const port = s && s.web_port, was = s && s.running_web_port;
    const moved = port && was && port !== was && location.protocol === "http:" && +location.port === was;
    const target = moved ? `${location.protocol}//${location.hostname}:${port}` : location.origin;
    msg(moved ? t("Wird neu gestartet … danach geht es unter Port {0} weiter.", port) : t("Wird neu gestartet …"));
    await sleep(2500);
    for (const t0 = Date.now(); Date.now() - t0 < 90000; await sleep(1000)) {
      try {
        if (moved) { await fetch(target + "/dsvr/api/health", { mode: "no-cors", cache: "no-store" }); location.href = target + "/dsvr/"; return; }
        const j = await (await fetch("/dsvr/api/health", { cache: "no-store" })).json();
        if (j.started && j.started !== before) { location.reload(); return; }
      } catch (e) { /* noch nicht erreichbar */ }
    }
    btn.disabled = false;
    msg(t("Keine Antwort nach dem Neustart – bitte im Paket-Zentrum prüfen, ob DS Video Revival läuft."));
  });

  // ---------- Jellyfin-Apps ----------
  async function loadJellyfin() {
    let r;
    try { r = await api("jellyfin"); } catch (e) { $("#jfDevMsg").textContent = t("Fehler: {0}", t(e.message)); return; }
    const host = location.hostname.includes(":") ? `[${location.hostname}]` : location.hostname;
    let html;
    if (!r.enabled) {
      html = `<p class="muted">${esc(t("Die Jellyfin-Schnittstelle ist ausgeschaltet."))}${r.admin ? " " + esc(t("Einschalten unter „Einstellungen“.")) : ""}</p>`;
    } else {
      const addrs = [[t("Im Heimnetz"), `http://${host}:${r.port}`], [t("Über DSM (HTTPS)"), `https://${host}:5001/jellyfin`]];
      html = addrs.map(([l, a]) => `<div class="jfaddr"><span class="lbl">${esc(l)}</span><button type="button" class="pathcopy" data-copy="${esc(a)}" title="${esc(t("Anklicken zum Kopieren"))}">${esc(a)}</button></div>`).join("")
        + `<p class="muted">${esc(t("Von unterwegs: deine DSM-Adresse (eigene Domain bzw. DDNS) mit /jellyfin, z.B. https://nas.example.com:5001/jellyfin. Synology-QuickConnect-IDs funktionieren in Jellyfin-Apps nicht. Infuse und Jellyfin für Android TV finden den Server im Heimnetz auch selbst."))}</p>`
        + (r.listening ? "" : `<p class="err">${esc(t("Port {0} ist nicht verfügbar – siehe Protokoll. Erreichbar bleibt die Adresse über DSM.", r.port))}</p>`);
    }
    $("#jfAddrs").innerHTML = html;
    wireCopy($("#jfAddrs"));
    $("#qcForm").hidden = !r.enabled;
    const devs = r.devices || [];
    $("#jfDevRows").innerHTML = devs.map((d) => `<tr><td>${esc(d.account)}</td><td>${esc(d.label)}${d.version ? `<span class="sub">${esc(t("Version {0}", d.version))}</span>` : ""}</td>
      <td data-l="${esc(t("Angemeldet seit"))}">${esc(connTime(d.created))}</td><td data-l="${esc(t("Zuletzt aktiv"))}">${esc(ago(d.last_seen))}${d.ip ? `<span class="sub">${esc(d.ip)}</span>` : ""}</td>
      <td><button type="button" class="secondary small" data-jfdel="${esc(d.id)}" data-who="${esc(d.account + " – " + d.label)}">${esc(t("Abmelden"))}</button></td></tr>`).join("")
      || `<tr><td colspan="5" class="muted">${esc(t("Keine angemeldeten Geräte."))}</td></tr>`;
    $$("#jfDevRows [data-jfdel]").forEach((b) => b.addEventListener("click", async () => {
      if (!window.confirm(t("„{0}“ abmelden? In der App ist danach eine neue Anmeldung nötig.", b.dataset.who))) return;
      b.disabled = true;
      try { await api("jellyfin?id=" + encodeURIComponent(b.dataset.jfdel), { method: "DELETE" }); $("#jfDevMsg").textContent = t("„{0}“ abgemeldet.", b.dataset.who); }
      catch (e) { $("#jfDevMsg").textContent = t("Fehler: {0}", t(e.message)); }
      loadJellyfin();
    }));
  }
  $("#jfReload").addEventListener("click", () => { $("#jfDevMsg").textContent = ""; $("#qcMsg").textContent = ""; loadJellyfin(); });
  $("#qcForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const code = $("#qcCode").value.replace(/\D/g, "");
    if (code.length !== 6) { $("#qcMsg").textContent = t("Bitte den sechsstelligen Code aus der App eingeben."); return; }
    try {
      await api("jellyfin/quickconnect", { method: "POST", body: JSON.stringify({ code }) });
      $("#qcCode").value = ""; $("#qcMsg").textContent = t("Bestätigt – die App meldet sich jetzt an.");
      setTimeout(() => { if (!$('[data-panel="jellyfin"]').hidden) loadJellyfin(); }, 5000);
    } catch (e) { $("#qcMsg").textContent = t(e.message); }
  });

  $("#metaRefresh").addEventListener("click", async () => { await api("meta", { method: "POST", body: JSON.stringify({ action: "refresh", id: 0 }) }); $("#metaState").textContent = t("Wird neu geladen…"); });

  async function loadLog() { const r = await api("log"); $("#logBox").textContent = (r.lines || []).slice().reverse().join("\n"); $("#logBox").scrollTop = 0; } // neueste oben
  $("#reloadLog").addEventListener("click", loadLog);

  async function start() {
    if (!sid) { showLogin(); return; }
    try { me = await api("me"); }
    catch (e) { if (e.message !== NOT_LOGGED_IN) showLogin(t(e.message)); return; }
    $("#login").hidden = true; $("#tabs").hidden = false;
    $("#who").textContent = me.account + (me.admin ? " (" + t("Admin") + ")" : "");
    $("#logout").hidden = false;
    applyMe();
    buildKindTabs();
    showTab("library");
  }
  // Reiter nach Rechten und Einstellungen
  function applyMe() {
    if (!me) return;
    $$("[data-admin]").forEach((b) => (b.hidden = !me.admin));
    $$("[data-dsm]").forEach((b) => (b.hidden = !me.dsm_admin));
    $$("[data-jf]").forEach((b) => (b.hidden = !me.jellyfin));
  }
  // Sprache der Oberfläche (je Browser)
  $("#langSel").value = window.I18N.pref;
  $("#langSel").addEventListener("change", () => { window.I18N.setPref($("#langSel").value); location.reload(); });
  start();
  setInterval(() => { if (!$('[data-panel="status"]').hidden) loadStatus().catch(() => {}); }, 15000);
})();
