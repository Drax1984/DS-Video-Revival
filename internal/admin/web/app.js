"use strict";
(function () {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  let sid = "";
  try { sid = sessionStorage.getItem("dsvr_sid") || ""; } catch (e) { /* kein Speicher */ }
  let me = null, cfg = null, kind = "movie", libId = 0, lib = [], current = null, playing = null;
  const isApple = /iPhone|iPad|iPod|Macintosh/.test(navigator.userAgent) && !!document.createElement("video").canPlayType("application/vnd.apple.mpegurl");
  const client = isApple ? "apple" : "browser";

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const saveSid = (v) => { sid = v; try { v ? sessionStorage.setItem("dsvr_sid", v) : sessionStorage.removeItem("dsvr_sid"); } catch (e) {} };
  const fmtTime = (s) => { s = Math.floor(s || 0); const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60; return (h ? h + ":" + String(m).padStart(2, "0") : m) + ":" + String(x).padStart(2, "0"); };
  const fmtSize = (b) => b > 1e9 ? (b / 1e9).toFixed(1) + " GB" : (b / 1e6).toFixed(0) + " MB";
  const planLabel = { direct: "Direktwiedergabe", remux: "Umverpacken (HLS)", transcode: "Umwandeln nach H.264" };

  async function api(path, opts = {}) {
    const r = await fetch("/dsvr/api/" + path, { ...opts, headers: { "X-DSVR-SID": sid, "X-DSVR-SYNOTOKEN": synoToken(), "Content-Type": "application/json", ...(opts.headers || {}) } });
    if (r.status === 401) { showLogin(); throw new Error("Nicht angemeldet"); }
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

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
    loggingIn = true; btn.disabled = true; btn.textContent = "Anmelden …";
    try { await login(ev); } finally { loggingIn = false; btn.disabled = false; btn.textContent = "Anmelden"; }
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
        if (code === 403) { $("#otpRow").hidden = false; $("#loginErr").textContent = "Bitte 2FA-Code eingeben."; return; }
        $("#loginErr").textContent = code === 400 ? "Konto oder Passwort falsch."
          : code === 402 ? "Keine Berechtigung für DS Video Revival (DSM → Systemsteuerung → Anwendungsberechtigungen)."
          : "Anmeldung fehlgeschlagen (Code " + code + ").";
        return;
      }
      saveSid(j.data.sid);
      saveSynoToken(j.data.synotoken);
      await start();
    } catch (e) { $("#loginErr").textContent = "Server nicht erreichbar."; }
  }

  $("#logout").addEventListener("click", async () => {
    const body = new URLSearchParams({ api: "SYNO.API.Auth", version: "6", method: "logout", session: "DSVideoRevival", _sid: sid });
    try { await fetch("/webapi/entry.cgi", { method: "POST", body }); } catch (e) { /* trotzdem lokal abmelden */ }
    saveSid(""); try { sessionStorage.removeItem("dsvr_synotoken"); } catch (e) {}
    me = null; showLogin("Abgemeldet.");
  });

  function showTab(name) {
    $$("#tabs button").forEach((x) => x.classList.toggle("active", x.dataset.tab === name));
    $$("[data-panel]").forEach((p) => (p.hidden = p.dataset.panel !== name));
    ({ library: loadLibrary, tree: loadTree, status: loadStatus, folders: loadFolders, access: loadAccess, conns: loadConns, settings: loadSettings, log: loadLog }[name] || (() => {}))();
  }
  $$("#tabs button").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));

  // ---------- Mediathek ----------
  // Reiter: Standardbibliotheken (id 0) und eigene Bibliotheken
  const stdLibs = [["movie", "Filme"], ["tvshow", "Serien"], ["home_video", "Heimvideos"], ["tv_record", "TV-Aufnahmen"]];
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
      if (b.banned_until) msgs.push(`Dieses Gerät wurde vom Administrator getrennt und ist bis ${esc(connTime(b.banned_until))} Uhr gesperrt. Danach erscheinen die Medien hier wieder.`);
      else msgs.push(`Dein Konto ist gerade auf einem anderen Gerät aktiv${b.device ? ` (${esc(b.device)})` : ""}. Gleichzeitiges Schauen auf mehreren Geräten ist für dieses Konto nicht freigegeben – sobald dort ${b.idle_minutes || 5} Minuten nichts mehr läuft oder abgemeldet wird, erscheinen die Medien hier wieder.`);
      blockT = setTimeout(() => { if (!document.querySelector('[data-panel="library"]').hidden && $("#detail").hidden && $("#player").hidden) loadLibrary(); }, 30000);
    }
    if (me && me.meta && me.meta.running) msgs.push(`Poster, Inhalte und IMDb-Bewertungen werden geladen (${me.meta.done}/${me.meta.total}) – die Ansicht füllt sich nach und nach.`);
    if (me && me.ffmpeg && me.admin && (!me.aac || !me.h264)) msgs.push(`Das gefundene FFmpeg kann ${!me.aac ? "kein AAC" : ""}${!me.aac && !me.h264 ? " und " : ""}${!me.h264 ? "kein H.264" : ""} erzeugen. iPhone/Apple TV spielen H.264-Filme mit AC3-Ton trotzdem ab (Ton wird unverändert durchgereicht); für DTS-Ton, HEVC im Browser und die Wiedergabe hier im Browser bitte Paket „ffmpeg7“ (SynoCommunity) installieren.`);
    if (me && !me.ffmpeg) msgs.push("FFmpeg nicht gefunden: MKV/AVI sowie AC3/DTS-Ton können nicht umgewandelt werden. Abhilfe: Paket „ffmpeg7“ (SynoCommunity) installieren.");
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
    $("#libCount").textContent = list.length + " Titel";
    $("#grid").innerHTML = list.map((e) => {
      const fmtR = (v) => (v % 1 ? v.toFixed(1) : String(v));
      // Ein Sternchen-Schild: eigene Bewertung (blau), sonst IMDb-Empfehlung (gelb).
      const rec = e.imdb_rating || e.rating;
      const badge = e.my_rating ? `<span class="rate mine" title="Deine Bewertung">★ ${fmtR(e.my_rating)}</span>`
        : (rec ? `<span class="rate" title="IMDb-Bewertung (Empfehlung)">★ ${rec.toFixed(1)}</span>` : "");
      const sub = (e.kind === "tv_record" ? [fmtRec(e.recorded) || e.year, e.channel]
        : [e.year, e.kind === "tvshow" && e.episodes ? e.episodes + " Folgen" : "", (e.genres || []).slice(0, 2).join(", ")]).filter(Boolean).join(" · ");
      // Info (nur DSM-Administratoren): Dateipfad anzeigen und kopieren
      const info = me && me.dsm_admin && e.paths && e.paths.length ? `<button type="button" class="info" data-info="${e.kind}:${e.id}" aria-label="Dateipfad von ${esc(e.title)}">Info</button>` : "";
      return `<div class="tw"><button class="tile" data-id="${e.id}" data-kind="${e.kind}"><div class="pimg">${e.poster ? `<img loading="lazy" src="${esc(e.poster)}" alt="">` : `<div class="ph">${esc(e.title)}</div>`}${badge}</div><div class="t">${esc(e.title)}</div><div class="s">${esc(sub)}</div></button>${info}</div>`;
    }).join("") || `<p class="muted">Keine Titel. ${me && me.admin ? "Unter „Medienordner“ Ordner hinzufügen und einlesen." : ""}</p>`;
    $$("#grid .tile").forEach((t) => t.addEventListener("click", () => openDetail(t.dataset.kind, +t.dataset.id)));
    $$("#grid [data-info]").forEach((b) => b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const [k, id] = b.dataset.info.split(":"); const e = lib.find((x) => x.kind === k && x.id === +id);
      if (e) showPathPop(b, e.title, e.paths, k === "tvshow" ? "Ordnerpfad" : (e.paths.length > 1 ? "Dateipfade" : "Dateipfad"));
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
      if (!b.classList.contains("pathcopy")) { b.dataset.label = b.dataset.label || b.textContent; b.textContent = ok ? "✓ kopiert" : "Kopieren nicht möglich"; }
      clearTimeout(b._t); b._t = setTimeout(() => { b.classList.remove("copied", "nocopy"); if (b.dataset.label) b.textContent = b.dataset.label; }, 1800);
    }));
  }
  function showPathPop(anchor, title, paths, label) {
    const pop = $("#pathPop");
    pop.innerHTML = `<div class="pp-head"><b>${esc(title)}</b><button type="button" class="pp-x" aria-label="Schließen">✕</button></div>
      <div class="pp-l">${esc(label)} – anklicken zum Kopieren</div>${paths.map((p) => `<button type="button" class="pathcopy" data-copy="${esc(p)}">${esc(p)}</button>`).join("")}`;
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
    $("#treeMsg").textContent = "Lade …";
    try { tree = await api("tree"); } catch (e) { $("#treeMsg").textContent = e.message; return; }
    renderTree();
  }
  const kindNames = { movie: "Film", episode: "Serie", home_video: "Heimvideo", tv_record: "TV-Aufnahme" };
  const techLine = (f) => [f.width ? f.width + "×" + f.height : "", (f.vcodec || "").toUpperCase(), (f.acodec || "").toUpperCase(), f.duration ? fmtTime(f.duration) : "", fmtSize(f.size)].filter(Boolean).join(" · ");
  // Eine Datei (Ordner- und Doppelgänger-Ansicht)
  // beste Datei je Doppelgänger-Gruppe: Auflösung, dann Laufzeit, dann Größe
  const better = (a, b) => (a.width * a.height - b.width * b.height) || ((a.duration || 0) - (b.duration || 0)) || (a.size - b.size);
  function bestOf(g) { return g.reduce((k, f) => (better(f, k) > 0 ? f : k), g[0]); }
  // mode (nur Doppelgänger-Ansicht): "ign" = Knopf „Ignorieren“, "unign" = „Wieder anzeigen“
  function treeFileRow(f, withTech, keep, mode) {
    const open = f.kind === "episode" ? "tvshow:" + f.show_id : f.kind + ":" + f.id;
    const info = withTech ? techLine(f) : [f.sub, fmtSize(f.size)].filter(Boolean).join(" · ");
    const dup = !withTech && f.dups > 1 ? ` <span class="dupb" title="Diesen Titel gibt es ${f.dups}-mal">${f.dups}×</span>` : keep ? ` <span class="keepb" title="Bleibt bei „Alle Doppelgänger löschen“ erhalten">bleibt</span>` : "";
    return `<div class="tfile">${f.poster ? `<img loading="lazy" src="${esc(f.poster)}" alt="">` : `<span class="noimg"></span>`}
      <div class="tf"><button type="button" class="tt" data-open="${open}">${esc(f.title)}</button> <span class="muted">${esc(info)}</span>${dup}
      <button type="button" class="pathcopy" data-fs="${esc(f.path)}" title="Ordner in der File Station öffnen (neues Fenster)">${esc(f.path)}</button></div>
      <div class="tbtns">${mode === "ign" ? `<button type="button" class="secondary small" data-ign="${f.file_id}" title="Diese Datei künftig nicht mehr als Doppelgänger anzeigen">Ignorieren</button>`
        : mode === "unign" ? `<button type="button" class="secondary small" data-unign="${f.file_id}" title="Diese Datei wieder bei den Doppelgängern berücksichtigen">Wieder anzeigen</button>` : ""}
      <button type="button" class="danger small" data-del="${f.file_id}" title="Diese Datei löschen">Löschen</button></div></div>`;
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
      msg = list.length ? `${list.length} Titel mit mehreren Dateien (${files} Dateien)` : "";
      html = list.map((g) => { const k = bestOf(g); return `<div class="tdup"><div class="tdhead"><b>${esc(g[0].title)}</b> <span class="muted">${esc([kindNames[g[0].kind], g[0].sub].filter(Boolean).join(" · "))}</span><span class="dupb">${g.length}×</span></div>`
        + [k, ...g.filter((f) => f !== k).sort((a, b) => better(b, a))].map((f) => treeFileRow(f, true, f === k, "ign")).join("") + `</div>`; }).join("")
        || `<p class="muted">${tree.length ? "Keine Doppelgänger gefunden." : "Noch keine Medienordner."}</p>`;
      // Ignorierte Dateien (rückgängig machen)
      const ign = [];
      tree.forEach((root) => root.dirs.forEach((d) => d.files.forEach((f) => { if (f.ignored && treeHit(f)) ign.push(f); })));
      if (ign.length) {
        ign.sort((a, b) => (a.title + (a.sub || "")).localeCompare(b.title + (b.sub || ""), "de"));
        html += `<details class="tdup tign"${ignOpen ? " open" : ""}><summary><b>Ignorierte Dateien</b> <span class="muted">${ign.length} – zählen nicht als Doppelgänger</span></summary>`
          + ign.map((f) => treeFileRow(f, true, false, "unign")).join("") + `</details>`;
      }
    } else {
      let shown = 0;
      html = tree.map((root) => {
        const dirs = root.dirs.map((d) => ({ ...d, files: d.files.filter(treeHit) })).filter((d) => d.files.length);
        if (filtered && !dirs.length) return "";
        const n = dirs.reduce((a, d) => a + d.files.length, 0); shown += n;
        return `<div class="troot"><div class="trhead"><button type="button" class="pathcopy strong" data-copy="${esc(root.path)}">${esc(root.path)}</button><span class="badge">${esc(root.library)}</span><span class="muted">${n} Datei${n === 1 ? "" : "en"}</span></div>`
          + (dirs.map((d) => `<details class="tdir"${treeOpen || filtered ? " open" : ""}><summary><span class="dn">📁 ${esc(d.rel || "(direkt im Medienordner)")}</span><span class="muted">${d.files.length}</span><button type="button" class="link" data-copy="${esc(d.path)}">Ordnerpfad kopieren</button></summary>`
            + d.files.map((f) => treeFileRow(f, false)).join("") + `</details>`).join("") || `<p class="muted">Keine Medien in diesem Ordner.</p>`)
          + `</div>`;
      }).join("") || `<p class="muted">${tree.length ? "Keine Treffer." : "Noch keine Medienordner."}</p>`;
      msg = filtered ? `${shown} Treffer` : "";
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
      $("#treeMsg").textContent = "Setze Schreibrecht …";
      const g = await api("grant", { method: "POST", body: JSON.stringify({ path: r.path, write: true }) });
      if (!g.ok) return g;
      r = await run();
    }
    return r;
  }
  const askWrite = (r) => confirm(`${r.message}\n\nSchreibrecht für DS Video Revival auf „${r.share}“ jetzt in DSM setzen?`);
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
      if (!r.ok) throw new Error(r.error || r.message || "Fehler");
    } catch (e) { $("#treeMsg").textContent = "Fehler: " + e.message; if (btn) btn.disabled = false; return; }
    // Zähler der Gruppe neu berechnen (ignorierte zählen nicht)
    f.ignored = on;
    const same = [];
    tree.forEach((r) => r.dirs.forEach((d) => d.files.forEach((x) => { if (x.dup === f.dup && !x.ignored) same.push(x); })));
    same.forEach((x) => { x.dups = same.length; });
    if (on) f.dups = 0;
    renderTree();
    $("#treeMsg").textContent = (on ? "Wird künftig nicht mehr als Doppelgänger angezeigt: " : "Wieder bei den Doppelgängern berücksichtigt: ") + f.path;
  }

  async function deleteTreeFile(fid) {
    let f; tree.forEach((r) => r.dirs.forEach((d) => d.files.forEach((x) => { if (x.file_id === fid) f = x; })));
    if (!f) return;
    const where = f.recycle ? "Sie wird in den Papierkorb des freigegebenen Ordners verschoben." : "Sie wird ENDGÜLTIG gelöscht (kein Papierkorb aktiv). Das lässt sich nicht rückgängig machen.";
    if (!confirm(`Diese Datei wirklich löschen?\n\n„${f.title}“${f.sub ? " (" + f.sub + ")" : ""}\n${f.path}\n${fmtSize(f.size)}\n\n${where}`)) return;
    try {
      const r = await deleteRequest(fid, askWrite);
      if (!r.ok) { $("#treeMsg").textContent = r.message; alert(r.message); return; }
      dropFromTree([fid]);
      renderTree();
      $("#treeMsg").textContent = (r.via_dsm ? "Über DSM gelöscht (wie in der File Station): " : f.recycle ? "In den Papierkorb verschoben: " : "Gelöscht: ") + f.path;
    } catch (e) { $("#treeMsg").textContent = "Löschen fehlgeschlagen: " + e.message; }
  }

  // Alle angezeigten Doppelgänger löschen, je Titel bleibt die beste Datei.
  let dupShown = [];
  $("#dupDelAll").addEventListener("click", async () => {
    const victims = dupShown.flatMap((g) => { const k = bestOf(g); return g.filter((f) => f !== k); });
    if (!victims.length) return;
    const size = victims.reduce((a, f) => a + f.size, 0), final = victims.filter((f) => !f.recycle).length;
    if (!confirm(`${victims.length} Doppelgänger-Dateien aus ${dupShown.length} Titeln löschen (zusammen ${fmtSize(size)})?\n\nJe Titel bleibt die beste Datei erhalten (höchste Auflösung, dann längste Laufzeit, dann größte Datei) – in der Liste mit „bleibt“ markiert.`
      + (final ? `\n\n${final} Datei(en) liegen in Freigaben ohne aktiven Papierkorb und werden ENDGÜLTIG gelöscht.` : "\n\nAlle Dateien kommen in den Papierkorb der jeweiligen Freigabe."))) return;
    const btn = $("#dupDelAll"); btn.disabled = true;
    const asked = new Map(), done = [], errs = [];
    const ask = async (r) => { if (!asked.has(r.share)) asked.set(r.share, askWrite(r)); return asked.get(r.share); };
    for (const [i, f] of victims.entries()) {
      $("#treeMsg").textContent = `Lösche ${i + 1} von ${victims.length} …`;
      try {
        const r = await deleteRequest(f.file_id, ask);
        if (r.ok) done.push(f.file_id); else errs.push(`${f.path}: ${r.message}`);
      } catch (e) { errs.push(`${f.path}: ${e.message}`); }
    }
    btn.disabled = false;
    dropFromTree(done);
    renderTree();
    const msg = `${done.length} von ${victims.length} Doppelgänger-Dateien gelöscht.` + (errs.length ? ` ${errs.length} nicht gelöscht.` : "");
    $("#treeMsg").textContent = msg;
    if (errs.length) alert(msg + "\n\n" + errs.slice(0, 10).join("\n") + (errs.length > 10 ? `\n… und ${errs.length - 10} weitere` : ""));
  });

  let treeT;
  $("#treeSearch").addEventListener("input", () => { clearTimeout(treeT); treeT = setTimeout(renderTree, 150); });
  $("#treeKind").addEventListener("change", renderTree);
  $("#treeView").addEventListener("change", renderTree);
  $("#treeToggle").addEventListener("click", () => {
    treeOpen = !treeOpen; $("#treeToggle").textContent = treeOpen ? "Alle zuklappen" : "Alle aufklappen";
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
    if (d.recorded) facts.push(`<span>Aufgenommen ${esc(fmtRec(d.recorded))}</span>`); else if (year) facts.push(`<span>${esc(year)}</span>`);
    if (d.channel) facts.push(`<span class="badge">${esc(d.channel)}</span>`);
    if (m && m.certificate) facts.push(`<span class="badge">${esc(m.certificate)}</span>`);
    if (m && m.runtime) facts.push(`<span>${Math.floor(m.runtime / 60)} h ${m.runtime % 60} min</span>`);
    if (m && m.genres && m.genres.length) facts.push(`<span>${esc(m.genres.join(", "))}</span>`);
    if (m && m.imdb_id) facts.push(`<a class="badge imdb" href="https://www.imdb.com/title/${esc(m.imdb_id)}/" target="_blank" rel="noopener">IMDb${m.imdb_rating ? " " + m.imdb_rating.toFixed(1) : ""}</a>`);
    if (m && m.rating) facts.push(`<span class="badge">${m.source === "tvmaze" || m.tvmaze_id ? "TVmaze" : "TMDb"} ${m.rating.toFixed(1)}</span>`);
    $("#dFacts").innerHTML = facts.join("");
    renderMyRating(d, m);
    $("#dTagline").textContent = (m && m.tagline) || "";
    $("#dSummary").textContent = (m && m.summary) || (m ? "" : "Keine Metadaten gefunden.");
    const ppl = [];
    if (m && m.directors && m.directors.length) ppl.push(`<div><b>Regie:</b> ${esc(m.directors.join(", "))}</div>`);
    if (m && m.writers && m.writers.length) ppl.push(`<div><b>Drehbuch:</b> ${esc(m.writers.slice(0, 4).join(", "))}</div>`);
    if (m && m.actors && m.actors.length) ppl.push(`<div><b>Darsteller:</b> ${esc(m.actors.slice(0, 8).join(", "))}</div>`);
    $("#dPeople").innerHTML = ppl.join("");
    $("#dEpisodes").innerHTML = ""; $("#dPlay").innerHTML = ""; $("#dFiles").innerHTML = ""; $("#dTech").textContent = "";
    if (d.kind === "tvshow") renderEpisodes(d); else renderFiles(d.files, d.title);
    $("#dAdmin").hidden = !(me && me.admin) || d.kind === "episode";
    if (me && me.admin) { renderFskSelect(d); renderTitleEdit(d); renderDelete(d); }
    $("#assignForm").reset(); // keine Eingabe vom zuvor geöffneten Titel übernehmen
    $("#assignMsg").textContent = m ? "Aktuell: " + [m.imdb_id ? "IMDb " + m.imdb_id : "", m.tmdb_id ? "TMDb " + m.tmdb_id : "", "Quelle " + (m.source || "?")].filter(Boolean).join(", ") + (m.locked ? " (manuell)" : "") : "Kein Treffer – IMDb-ID (tt…) eintragen.";
    $("#detail").hidden = false;
    $("#dClose").focus();
  }

  // Titel von Hand ändern (gilt auch in den DS-video-Apps)
  function renderTitleEdit(d) {
    $("#dTitleIn").value = d.custom_title || d.title || "";
    $("#dTitleMsg").textContent = d.custom_title ? "von Hand gesetzt" : "automatisch (Dateiname bzw. Metadaten)";
    const save = async (t) => {
      try {
        const r = await api("title", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, title: t }) });
        d.title = r.title; d.custom_title = r.custom_title;
        $("#dTitle").textContent = r.title; renderTitleEdit(d); $("#dTitleMsg").textContent = "Gespeichert.";
        const e = lib.find((x) => x.id === d.id && x.kind === d.kind); if (e) { e.title = r.title; renderGrid(); }
      } catch (e) { $("#dTitleMsg").textContent = e.message; }
    };
    $("#titleForm").onsubmit = (ev) => { ev.preventDefault(); save($("#dTitleIn").value.trim()); };
    $("#dTitleReset").onclick = () => save("");
  }

  // Titel löschen (nur Admin): Papierkorb der Freigabe, sonst endgültig.
  function renderDelete(d) {
    $("#dDelBox").hidden = !d.deletable;
    $("#dDelMsg").textContent = d.recycle ? "Datei(en) kommen in den Papierkorb des freigegebenen Ordners." : "Kein Papierkorb aktiv – Datei(en) werden endgültig gelöscht.";
    $("#dDelete").onclick = async () => {
      const n = (d.files || []).length;
      const what = n > 1 ? n + " Dateien" : "die Datei";
      if (!confirm(`„${d.title}“ wirklich löschen?\n\n${d.recycle ? `Es wird ${what} in den Papierkorb des freigegebenen Ordners verschoben.` : `Es wird ${what} ENDGÜLTIG gelöscht (kein Papierkorb aktiv). Das lässt sich nicht rückgängig machen.`}`)) return;
      const run = async () => api("delete", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id }) });
      try {
        let r = await run();
        if (!r.ok && r.need_write) {
          if (!confirm(`${r.message}\n\nSchreibrecht für DS Video Revival auf „${r.share}“ jetzt in DSM setzen?`)) { $("#dDelMsg").textContent = r.message; return; }
          $("#dDelMsg").textContent = "Setze Schreibrecht …";
          const g = await api("grant", { method: "POST", body: JSON.stringify({ path: r.path, write: true }) });
          if (!g.ok) { $("#dDelMsg").textContent = g.message; return; }
          r = await run();
        }
        if (!r.ok) { $("#dDelMsg").textContent = r.message; return; }
        $("#detail").hidden = true;
        lib = lib.filter((x) => !(x.id === d.id && x.kind === d.kind)); renderGrid();
        $("#libMsg").textContent = r.message;
      } catch (e) { $("#dDelMsg").textContent = e.message; }
    };
  }

  // FSK von Hand setzen (Jugendschutz)
  function renderFskSelect(d) {
    const sel = $("#dFsk");
    const auto = d.fsk_manual ? "automatisch (aus Metadaten)" : "automatisch: " + (d.fsk >= 0 ? "FSK " + d.fsk : "unbekannt");
    sel.innerHTML = `<option value="">${auto}</option>` + [0, 6, 12, 16, 18].map((n) => `<option value="${n}"${d.fsk_manual && d.fsk === n ? " selected" : ""}>FSK ${n}</option>`).join("");
    $("#dFskMsg").textContent = d.fsk_manual ? "von Hand gesetzt" : "";
    sel.onchange = async () => {
      try {
        const r = await api("fsk", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, fsk: sel.value === "" ? null : +sel.value }) });
        d.fsk = r.fsk; d.fsk_manual = r.fsk_manual; renderFskSelect(d); $("#dFskMsg").textContent = "Gespeichert.";
      } catch (e) { $("#dFskMsg").textContent = e.message; }
    };
  }

  // Eigene Bewertung 1–10 (gleiche Skala wie IMDb); IMDb dient als Empfehlung.
  function renderMyRating(d, m) {
    const v = d.my_rating || 0;
    const stars = Array.from({ length: 10 }, (_, i) => `<button type="button" data-r="${i + 1}" class="${i < Math.round(v) ? "on" : ""}" aria-label="${i + 1} von 10">★</button>`).join("");
    const rec = m && m.imdb_rating ? `Empfehlung (IMDb): ★ ${m.imdb_rating.toFixed(1)}` : (m && m.rating ? `Empfehlung: ★ ${m.rating.toFixed(1)}` : "");
    $("#dRate").innerHTML = `<span>Deine Bewertung:</span><span class="stars" role="group" aria-label="Deine Bewertung – Stern anklicken">${stars}</span><span class="val">${v ? (v % 1 ? v.toFixed(1) : v) + "/10" : "noch keine – Stern anklicken"}</span>${v ? '<button type="button" class="link" data-r="0">entfernen</button>' : ""}<span class="muted">${esc(rec)}</span><span class="err" id="rateErr"></span>`;
    const box = $("#dRate .stars");
    $$("#dRate .stars button").forEach((b) => {
      b.addEventListener("mouseenter", () => $$("#dRate .stars button").forEach((x) => x.classList.toggle("on", +x.dataset.r <= +b.dataset.r)));
    });
    box.addEventListener("mouseleave", () => $$("#dRate .stars button").forEach((x) => x.classList.toggle("on", +x.dataset.r <= Math.round(v))));
    $$("#dRate [data-r]").forEach((b) => b.addEventListener("click", async () => {
      const r = +b.dataset.r;
      try { await api("rate", { method: "POST", body: JSON.stringify({ kind: d.kind, id: d.id, rating: r }) }); }
      catch (e) { $("#rateErr").textContent = "Bewertung nicht gespeichert: " + e.message; return; }
      d.my_rating = r; renderMyRating(d, m);
      const e = lib.find((x) => x.id === d.id && x.kind === d.kind); if (e) { e.my_rating = r; renderGrid(); }
    }));
  }

  function renderFiles(files, title) {
    if (!files || !files.length) return;
    const f = files[0];
    const btn = [];
    if (f.position > 30) btn.push(`<button data-play="${f.id}" data-start="${f.position}">▶ Fortsetzen ab ${fmtTime(f.position)}</button>`);
    btn.push(`<button data-play="${f.id}" data-start="0" ${f.position > 30 ? 'class="secondary"' : ""}>▶ Abspielen</button>`);
    $("#dPlay").innerHTML = btn.join("");
    // Ergänzend unter der Beschreibung: Auflösung und Dateigröße
    $("#dTech").textContent = fileTech(files);
    // Keine Dateinamen/Speicherorte; bei mehreren Fassungen nur zur Auswahl.
    $("#dFiles").innerHTML = files.length < 2 ? "" : `<ul class="files">${files.map((x, i) => {
      const pr = x.probe || {};
      const tech = [pr.width ? pr.width + "×" + pr.height : "", fmtSize(x.size)].filter(Boolean).join(" · ");
      return `<li>Fassung ${i + 1}${tech ? " · " + esc(tech) : ""} <button class="secondary" data-play="${x.id}" data-start="0">abspielen</button></li>`;
    }).join("")}</ul>`;
    $$("[data-play]", $("#detail")).forEach((b) => b.addEventListener("click", () => play(+b.dataset.play, +b.dataset.start, title)));
  }

  // fileTech: „Auflösung: 1920×1080 (Full HD) · Dateigröße: 2.1 GB“ (erste Datei)
  function fileTech(files) {
    if (!files || !files.length) return "";
    const f = files[0], pr0 = f.probe || {};
    const tech = [];
    if (pr0.width) tech.push(`Auflösung: ${pr0.width}×${pr0.height}${resLabel(pr0.width, pr0.height)}`);
    const total = files.reduce((a, x) => a + (x.size || 0), 0);
    tech.push(`Dateigröße: ${fmtSize(f.size)}${files.length > 1 ? ` (${files.length} Dateien, zusammen ${fmtSize(total)})` : ""}`);
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
    $("#dEpisodes").innerHTML = Object.keys(seasons).sort((a, b) => a - b).map((s) => `<div class="season"><h4>Staffel ${s}</h4>${seasons[s].map((e) => {
      const f = (e.files || [])[0];
      return `<div class="ep">${e.still ? `<img src="${esc(e.still)}" alt="" loading="lazy">` : ""}<div class="eb"><div class="et">${e.episode}. ${esc(e.title || "Folge " + e.episode)}</div>${e.summary ? `<p class="es">${esc(e.summary)}</p>` : ""}${f ? `<p class="etech">${esc(fileTech(e.files))}</p>` : ""}${f && f.position > 30 ? `<p>Gesehen bis ${fmtTime(f.position)}</p>` : ""}</div>${f ? `<button data-play="${f.id}" data-start="${f.position > 30 ? f.position : 0}" data-title="${esc(d.title + " – S" + e.season + "E" + e.episode)}">▶</button>` : ""}</div>`;
    }).join("")}</div>`).join("") || `<p class="muted">Keine Folgen.</p>`;
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
    $("#assignMsg").textContent = "Wird geladen…";
    try { const r = await api("meta", { method: "POST", body: JSON.stringify({ action: "assign", kind: current.kind, id: current.id, ref }) }); $("#assignMsg").textContent = "Zugeordnet: " + r.title; openDetail(current.kind, current.id); loadLibrary(); }
    catch (e) { $("#assignMsg").textContent = "Fehler: " + e.message; }
  });
  $("#dRefresh").addEventListener("click", async () => { await api("meta", { method: "POST", body: JSON.stringify({ action: "refresh", id: current.id }) }); $("#assignMsg").textContent = "Wird neu geladen – in einigen Sekunden erneut öffnen."; });

  // ---------- Player ----------
  let hls = null, progT = null;
  async function play(fileId, startAt, title) {
    const v = $("#video");
    $("#pTitle").textContent = title || (current && current.title) || "";
    $("#pMsg").textContent = "Wird vorbereitet…";
    $("#pMode").textContent = "";
    $("#player").hidden = false;
    let r;
    try { r = await api("play", { method: "POST", body: JSON.stringify({ file_id: fileId, client, start: startAt }) }); }
    catch (e) { $("#pMsg").textContent = "Wiedergabe nicht möglich: " + e.message; return; }
    playing = { token: r.token, fileId, offset: r.offset || 0 };
    $("#pMode").textContent = planLabel[r.mode] || r.mode;
    $("#pMsg").textContent = r.mode === "direct" ? "" : r.reason;
    const startPlayback = () => { v.play().catch(() => {}); };
    if (r.url.endsWith(".m3u8")) {
      attachHLS(v, r.url, startPlayback, r.seek || 0);
    } else {
      v.src = r.url;
      v.addEventListener("loadedmetadata", () => { if (startAt > 0) v.currentTime = startAt; startPlayback(); }, { once: true });
      v.addEventListener("error", () => { if (playing) $("#pMsg").innerHTML = 'Direktwiedergabe fehlgeschlagen. <button id="forceTc" class="secondary">Umwandeln</button>'; const b = $("#forceTc"); if (b) b.onclick = () => forceTranscode(fileId, startAt, title); }, { once: true });
    }
    clearInterval(progT);
    progT = setInterval(saveProgress, 15000);
  }

  async function forceTranscode(fileId, startAt, title) {
    await stopPlayback();
    $("#player").hidden = false;
    const r = await api("play", { method: "POST", body: JSON.stringify({ file_id: fileId, client, start: startAt, force: "transcode" }) }).catch((e) => ({ error: e.message }));
    if (r.error) { $("#pMsg").textContent = r.error; return; }
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
      hls.on(window.Hls.Events.ERROR, (_, data) => { if (data.fatal) $("#pMsg").textContent = "Wiedergabefehler: " + data.details; });
      hls.loadSource(url); hls.attachMedia(v);
      hls.on(window.Hls.Events.MANIFEST_PARSED, onReady);
    } else if (v.canPlayType("application/vnd.apple.mpegurl")) {
      v.src = url; v.addEventListener("loadedmetadata", () => { if (seek > 0) v.currentTime = seek; onReady(); }, { once: true });
    } else { $("#pMsg").textContent = "Dieser Browser kann HLS nicht abspielen."; }
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
    const up = s.upstream_ok == null ? "–" : (s.upstream_ok ? "erreichbar" : "NICHT erreichbar");
    $("#statusGrid").innerHTML = [
      kpi("Version", s.version), kpi("Plattform", s.arch), kpi("DSM-WebAPI", up), kpi("Dateien", lb.files ?? 0),
      kpi("Filme", items.movie ?? 0), kpi("Serien / Folgen", (lb.shows ?? 0) + " / " + (items.episode ?? 0)), kpi("Heimvideos", items.home_video ?? 0), kpi("TV-Aufnahmen", items.tv_record ?? 0),
      kpi("Metadaten (" + (md.source === "tmdb" ? "TMDb" : "Wikidata/Wikipedia") + ")", md.running ? `läuft ${md.done}/${md.total}` : `${md.found} gefunden, ${md.not_found} nicht`),
      kpi("IMDb-Bewertungen", md.rating_error ? "Fehler" : (md.ratings ?? 0) + " aktualisiert"),
      kpi("FFmpeg", s.ffmpeg ? "vorhanden" : "fehlt"),
      kpi("Umwandlung H.264", !(s.transcode || {}).available ? "nicht möglich" : (s.transcode.vaapi ? "Hardware (VAAPI)" : s.transcode.h264) + (s.transcode.aac ? " + AAC" : " (kein AAC!)")), kpi("Umwandlungen aktiv", s.hls_jobs), kpi("Sitzungen", s.sessions),
      kpi("Letzter Scan", lb.last_scan && !lb.last_scan.startsWith("0001") ? new Date(lb.last_scan).toLocaleString("de-DE") : (lb.scanning ? "läuft…" : "noch nie")),
    ].join("");
    const errs = lb.scan_errors || [];
    $("#scanErrors").innerHTML = (md.last_error || md.rating_error ? `<h3>Metadaten</h3><p class="err">${esc([md.last_error, md.rating_error].filter(Boolean).join(" · "))}</p>` : "") + (errs.length ? "<h3>Hinweise beim letzten Scan</h3><ul>" + errs.slice(0, 50).map((e) => `<li>${esc(e)}</li>`).join("") + "</ul>" : "");
  }
  $("#scanBtn").addEventListener("click", async () => { const r = await api("scan", { method: "POST" }); $("#scanBtn").textContent = r.message; setTimeout(() => { $("#scanBtn").textContent = "Bibliothek jetzt einlesen"; loadStatus(); }, 3000); });

  const typeLabel = { movie: "Filme", tvshow: "Serien", home_video: "Heimvideos", tv_record: "TV-Aufnahmen" };
  // Auswahl „Bibliothek“ für Medienordner: Standardbibliotheken + eigene (Wert typ:id)
  function libOptions(sel) {
    return stdLibs.map(([t, l]) => `<option value="${t}:0"${sel === t + ":0" ? " selected" : ""}>${l}</option>`).join("")
      + ((cfg && cfg.libraries) || []).map((l) => `<option value="${esc(l.type)}:${l.id}"${sel === l.type + ":" + l.id ? " selected" : ""}>${esc(l.title)} (${typeLabel[l.type]})</option>`).join("");
  }
  const libName = (t, id) => (id ? (((cfg && cfg.libraries) || []).find((l) => l.id === id) || {}).title || "?" : typeLabel[t]);
  // ---------- Benutzerrechte ----------
  let acc = null;
  let gEdit = null; // {old, name, members}
  const whoLabel = (k) => (k.startsWith("vgroup:") ? "Gruppe " : k.startsWith("group:") ? "DSM-Gruppe " : "") + k.replace(/^(user|group|vgroup):/, "");
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
      <div class="panel" hidden>${sections.filter((x) => x.items.length).map((x) => `<h5>${esc(x.title)}</h5>` + x.items.map((k) => `<label><input type="checkbox" value="${esc(k)}"${selected.includes(k) ? " checked" : ""}>${esc(k.replace(/^(user|group|vgroup):/, ""))}</label>`).join("")).join("") || '<p class="muted" style="padding:6px 12px">Keine Einträge.</p>'}</div>`;
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
    const sections = [{ title: "Gruppen", items: vgroups }, { title: "DSM-Gruppen", items: dsmGroups }, { title: "Benutzer", items: [...users, ...extra.filter((k) => k.startsWith("user:"))] }];

    // Gruppen: Anlegen, Bearbeiten (Name, Mitglieder) und Löschen werden sofort gespeichert.
    const gl = Object.keys(acc.vgroups).filter((g) => g !== AG).sort();
    const gForm = () => `<div class="gedit">${gEdit.old === AG ? "" : `<label>Name <input type="text" id="gName" value="${esc(gEdit.name)}" maxlength="64"></label>`}<span id="gMembers"></span>
        <button type="button" id="gOk">${gEdit.old ? "Speichern" : "Anlegen"}</button><button type="button" id="gCancel" class="secondary">Abbrechen</button><span id="gMsg" class="err"></span></div>`;
    // Administratoren (eigener Abschnitt, nicht löschbar)
    $("#accAdmins").innerHTML = gEdit && gEdit.old === AG ? gForm()
      : `<div class="accrow"><div><span class="members">${esc((acc.vgroups[AG] || []).join(", ") || "keine weiteren Mitglieder")} · dazu immer die DSM-Administratoren</span></div>
        <div class="row"><button type="button" class="secondary small" data-gedit="${esc(AG)}">Mitglieder bearbeiten</button></div></div>`;
    // Eigene Gruppen
    $("#accGroups").innerHTML = gl.map((g) => gEdit && gEdit.old === g ? gForm()
      : `<div class="accrow"><div><b>${esc(g)}</b> <span class="members">${esc((acc.vgroups[g] || []).join(", ") || "keine Mitglieder")}</span></div>
        <div class="row"><button type="button" class="secondary small" data-gedit="${esc(g)}">Bearbeiten</button><button type="button" class="secondary small" data-gdel="${esc(g)}">Löschen</button></div></div>`).join("")
      + (gEdit && !gEdit.old ? gForm() : "")
      || `<p class="muted">Noch keine eigenen Gruppen.</p>`;
    $("#accNewGroup").hidden = !!gEdit;
    if (gEdit) {
      multiDrop($("#gMembers"), [{ title: "Benutzer", items: users }], gEdit.members, "Mitglieder wählen");
      $("#gOk").addEventListener("click", async () => {
        const name = gEdit.old === AG ? AG : $("#gName").value.trim().toLowerCase();
        if (!name || /[:\n]/.test(name)) { $("#gMsg").textContent = "Bitte einen Namen ohne „:“ eingeben."; return; }
        if (name !== gEdit.old && acc.vgroups[name]) { $("#gMsg").textContent = "Gruppe gibt es schon."; return; }
        const members = gEdit.members.map((k) => k.replace(/^user:/, ""));
        const was = gEdit.old;
        if (was && was !== name) { renamePrincipal("vgroup:" + was, "vgroup:" + name); delete acc.vgroups[was]; }
        acc.vgroups[name] = members; gEdit = null;
        if (was === AG) await saveAccess("Administratoren gespeichert.", "#accAdminMsg");
        else await saveAccess(was ? (was !== name ? `Gruppe „${was}“ in „${name}“ umbenannt und gespeichert.` : `Gruppe „${name}“ gespeichert.`) : `Gruppe „${name}“ angelegt.`);
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
      if (!window.confirm(`Gruppe „${g}“ löschen? Ordnerfreigaben und Altersgrenzen dieser Gruppe entfallen.`)) return;
      delete acc.vgroups[g]; renamePrincipal("vgroup:" + g, null); gEdit = null;
      await saveAccess(`Gruppe „${g}“ gelöscht.`);
    }));

    // Ordnerfreigaben
    $("#accFolders").innerHTML = acc.folders.map((f, i) => `<div class="accrow"><code>${esc(f.path)}</code><div data-fd="${i}"></div></div>`).join("") || `<p class="muted">Noch keine Medienordner.</p>`;
    acc.folders.forEach((f, i) => multiDrop($(`[data-fd="${i}"]`), sections, f.allow, "Alle", () => { $("#accMsg").textContent = "Noch nicht gespeichert."; }));

    // Herunterladen (Download und Offline-Umwandlung in den Apps)
    multiDrop($("#accDownload"), sections, acc.download_allow, "Alle", () => { $("#accMsg").textContent = "Noch nicht gespeichert."; });
    // Nur eine Verbindung gleichzeitig
    multiDrop($("#accSingle"), sections, acc.single_session, "Niemand", () => { $("#accMsg").textContent = "Noch nicht gespeichert."; });
    $("#accIdle").value = acc.single_session_idle || 5;

    // Jugendschutz
    const keys = Object.keys(acc.restrictions).sort();
    $("#accRules").innerHTML = keys.map((k) => {
      const r = acc.restrictions[k];
      return `<tr><td>${esc(whoLabel(k))}</td><td><select data-max="${esc(k)}">${acc.levels.map((l) => `<option value="${l}"${r.max_fsk === l ? " selected" : ""}>FSK ${l}</option>`).join("")}</select></td>
        <td><label class="check"><input type="checkbox" data-unr="${esc(k)}"${r.block_unrated ? " checked" : ""}> sperren</label></td>
        <td><button type="button" class="secondary" data-delr="${esc(k)}">Entfernen</button></td></tr>`;
    }).join("") || `<tr><td colspan="4" class="muted">Keine Altersgrenzen – alle sehen alle Titel ihrer Ordner.</td></tr>`;
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
      $("#accMsg").textContent = "Gespeichert."; $("#accGroupMsg").textContent = ""; $("#accAdminMsg").textContent = ""; $(target).textContent = okMsg || "";
    } catch (e) {
      $("#accMsg").textContent = "Fehler: " + e.message; $(target).textContent = "Fehler: " + e.message;
      await loadAccess(); // verworfene Änderung nicht stehen lassen
    }
  }
  $("#accSave").addEventListener("click", () => saveAccess(""));

  // ---------- Verbindungen ----------
  const connState = { play: "spielt ab", active: "aktiv", idle: "ruhend", blocked: "gesperrt (2. Verbindung)", banned: "gesperrt" };
  try { const b = localStorage.getItem("dsvr_connBlock"); if (b !== null) $("#connBlock").value = b; } catch (e) { /* kein Speicher */ }
  $("#connBlock").addEventListener("change", () => { try { localStorage.setItem("dsvr_connBlock", $("#connBlock").value); } catch (e) {} });
  const blockLabel = () => $("#connBlock").selectedOptions[0].textContent;
  const connTime = (t) => { const d = new Date(t); if (isNaN(d)) return ""; const today = new Date().toDateString() === d.toDateString();
    return (today ? "" : d.toLocaleDateString("de-DE", { day: "2-digit", month: "2-digit" }) + " ") + d.toLocaleTimeString("de-DE", { hour: "2-digit", minute: "2-digit" }); };
  const ago = (t) => { const s = Math.max(0, Math.round((Date.now() - new Date(t)) / 1000)); return s < 60 ? "gerade eben" : s < 3600 ? `vor ${Math.round(s / 60)} Min.` : `vor ${Math.floor(s / 3600)} Std.`; };
  async function loadConns() {
    let r;
    try { r = await api("connections"); } catch (e) { $("#connMsg").textContent = "Fehler: " + e.message; return; }
    const list = r.connections || [];
    $("#connRows").innerHTML = list.map((c) => {
      const self = c.id === r.self;
      const kind = c.kind === "web" ? "Weboberfläche" : "App";
      return `<tr><td>${esc(c.account)}${c.single ? '<span class="one" title="Nur eine Verbindung gleichzeitig">nur 1</span>' : ""}</td>
        <td>${esc(c.device || kind)}<span class="sub">${esc(kind)}${c.ip ? " · " + esc(c.ip) : ""}</span></td>
        <td><span class="cst ${esc(c.state)}">${esc(c.state === "banned" ? "gesperrt bis " + connTime(c.until) : connState[c.state] || c.state)}</span></td>
        <td data-l="Titel">${esc(c.title || "–")}</td>
        <td data-l="Verbunden seit">${esc(connTime(c.started))}</td>
        <td data-l="Letzte Aktivität">${esc(ago(c.last_seen))}</td>
        <td>${self ? '<span class="muted">diese Sitzung</span>' : `<button type="button" class="danger small" data-kick="${esc(c.id)}" data-who="${esc(c.account + " – " + (c.device || kind))}">Trennen</button>`}</td></tr>`;
    }).join("") || `<tr><td colspan="7" class="muted">Keine aktiven Verbindungen.</td></tr>`;
    $$("#connRows [data-kick]").forEach((b) => b.addEventListener("click", async () => {
      const block = +$("#connBlock").value;
      if (!window.confirm(`Verbindung „${b.dataset.who}“ trennen${block ? " und das Gerät " + blockLabel() + " sperren" : ""}? Eine laufende Wiedergabe bricht ab.`)) return;
      b.disabled = true;
      try { await api("connections", { method: "POST", body: JSON.stringify({ id: b.dataset.kick, block_minutes: block }) }); $("#connMsg").textContent = `„${b.dataset.who}“ getrennt${block ? ", " + blockLabel() + " gesperrt" : ""}.`; }
      catch (e) { $("#connMsg").textContent = "Fehler: " + e.message; }
      loadConns();
    }));
    const bans = r.bans || [];
    $("#banBox").hidden = !bans.length;
    $("#banRows").innerHTML = bans.map((x) => `<tr><td>${esc(x.account)}</td><td>${esc(x.device || "–")}<span class="sub">${esc(x.ip || "")}</span></td>
      <td data-l="Gesperrt bis">${esc(connTime(x.until))}</td><td><button type="button" class="secondary small" data-unban="${esc(x.id)}">Sperre aufheben</button></td></tr>`).join("");
    $$("#banRows [data-unban]").forEach((b) => b.addEventListener("click", async () => {
      b.disabled = true;
      try { await api("connections", { method: "POST", body: JSON.stringify({ unban: b.dataset.unban }) }); $("#connMsg").textContent = "Sperre aufgehoben."; }
      catch (e) { $("#connMsg").textContent = "Fehler: " + e.message; }
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
      return `<tr><td><code>${esc(f.path)}</code></td><td><select data-type="${i}" aria-label="Bibliothek">${libOptions(f.type + ":" + (f.library_id || 0))}</select></td><td class="${ok ? "yes" : "no"}">${ok ? "ja" : "nein" + (r && r.error ? " – " + esc(r.error) : "")}</td><td><button class="secondary" data-del="${i}">Entfernen</button></td></tr>`;
    }).join("") || `<tr><td colspan="4" class="muted">Noch keine Ordner.</td></tr>`;
    $$("[data-del]").forEach((b) => b.addEventListener("click", () => { cfg.folders.splice(+b.dataset.del, 1); saveCfg(); }));
    $$("[data-type]").forEach((s) => s.addEventListener("change", () => {
      const f = cfg.folders[+s.dataset.type];
      const [t, id] = s.value.split(":");
      if (!window.confirm(`${f.path} der Bibliothek „${libName(t, +id)}“ zuordnen? Die Titel darin werden neu eingeordnet (gespeicherte Wiedergabepositionen dieser Dateien gehen dabei verloren).`)) { s.value = f.type + ":" + (f.library_id || 0); return; }
      f.type = t; f.library_id = +id; saveCfg();
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
      return `<div class="accrow"><div class="row wrap"><input data-lname="${i}" value="${esc(l.title)}" maxlength="64" aria-label="Name"><span class="muted">${typeLabel[l.type]} · ${n} Ordner</span></div>
        <div class="row"><button type="button" class="secondary small" data-lsave="${i}">Umbenennen</button><button type="button" class="secondary small" data-ldel="${i}">Löschen</button></div></div>`;
    }).join("") || `<p class="muted">Noch keine eigenen Bibliotheken.</p>`;
    $$("[data-lsave]").forEach((b) => b.addEventListener("click", () => {
      const l = libs[+b.dataset.lsave], name = $(`[data-lname="${b.dataset.lsave}"]`).value.trim();
      if (!name) { $("#libMsgCfg").textContent = "Bitte einen Namen eingeben."; return; }
      l.title = name; saveCfg();
    }));
    $$("[data-ldel]").forEach((b) => b.addEventListener("click", () => {
      const l = libs[+b.dataset.ldel];
      const fs = (cfg.folders || []).filter((f) => f.library_id === l.id);
      if (!window.confirm(`Bibliothek „${l.title}“ löschen?` + (fs.length ? `\n\nIhre ${fs.length} Ordner kommen in die Standardbibliothek „${typeLabel[l.type]}“ (Dateien bleiben erhalten).` : ""))) return;
      fs.forEach((f) => (f.library_id = 0));
      cfg.libraries = libs.filter((x) => x !== l); saveCfg();
    }));
  }
  $("#libNew").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const name = $("#libNewName").value.trim();
    if (!name) { $("#libMsgCfg").textContent = "Bitte einen Namen eingeben."; return; }
    cfg.libraries = cfg.libraries || [];
    if (cfg.libraries.some((l) => l.title.toLowerCase() === name.toLowerCase())) { $("#libMsgCfg").textContent = "Diese Bibliothek gibt es schon."; return; }
    const id = cfg.libraries.reduce((m, l) => Math.max(m, l.id), 0) + 1;
    cfg.libraries.push({ id, title: name, type: $("#libNewType").value, is_public: true });
    $("#libNewName").value = ""; $("#libMsgCfg").textContent = "";
    saveCfg();
  });
  async function saveCfg() {
    cfg.scan_interval_minutes = parseInt($("#scanInt").value || "0", 10);
    try { await api("config", { method: "PUT", body: JSON.stringify(cfg) }); $("#cfgMsg").textContent = "Gespeichert, Bibliothek wird neu eingelesen."; }
    catch (e) { $("#cfgMsg").textContent = "Fehler: " + e.message; }
    try { me = await api("me"); buildKindTabs(); } catch (e) {}
    loadFolders();
  }
  $("#saveCfg").addEventListener("click", saveCfg);
  // Ordnerauswahl: freigegebene Ordner durchblättern, Leserecht für den Paketbenutzer setzen.
  let pkCur = "";
  async function pkLoad(path) {
    $("#pkMsg").textContent = ""; $("#pkInfo").textContent = "Lade …"; $("#pkList").innerHTML = "";
    let r;
    try { r = await api("browse?path=" + encodeURIComponent(path)); }
    catch (e) {
      if (!path) { $("#pkInfo").textContent = ""; $("#pkMsg").textContent = e.message; return; }
      // Unterordner nicht auflistbar (noch kein Recht): Ordner selbst bleibt wählbar.
      const up = path.replace(/\/[^/]+$/, "");
      r = { dirs: [], parent: /^\/volume[^/]+$/.test(up) ? "" : up, readable: false, user: "dsvideorevival", note: "Unterordner werden nach dem Setzen des Leserechts sichtbar." };
    }
    pkCur = path;
    $("#pkPath").textContent = path || "Freigegebene Ordner";
    $("#pkUp").disabled = !path; $("#pkUp").dataset.to = r.parent || "";
    $("#pkAdd").disabled = !path;
    $("#pkList").innerHTML = (r.dirs || []).map((d) => `<li><button type="button" data-to="${esc(d.path)}"><span>📁 ${esc(d.name)}</span></button></li>`).join("")
      || `<li class="muted" style="padding:9px 12px">Keine Unterordner.</li>`;
    $$("#pkList [data-to]").forEach((b) => b.addEventListener("click", () => pkLoad(b.dataset.to)));
    $("#pkInfo").textContent = !path ? "Ordner antippen, um ihn zu öffnen." :
      (r.readable ? "" : "Das Leserecht für DS Video Revival wird beim Hinzufügen automatisch gesetzt." + (r.note ? " " + r.note : ""));
  }
  $("#addFolder").addEventListener("click", () => { $("#picker").hidden = false; pkLoad(""); $("#pkClose").focus(); });
  $("#pkClose").addEventListener("click", () => { $("#picker").hidden = true; });
  $("#pkUp").addEventListener("click", () => pkLoad($("#pkUp").dataset.to || ""));
  $("#pkAdd").addEventListener("click", async () => {
    const path = pkCur; if (!path) return;
    if ((cfg.folders || []).some((f) => f.path === path)) { $("#pkMsg").textContent = "Dieser Ordner ist bereits eingetragen."; return; }
    $("#pkAdd").disabled = true; $("#pkMsg").textContent = "";
    let g;
    try { g = await api("grant", { method: "POST", body: JSON.stringify({ path }) }); }
    catch (e) { g = { ok: false, message: e.message }; }
    $("#pkAdd").disabled = false;
    if (!g.ok) { $("#pkMsg").textContent = g.message; return; }
    const [t, lid] = $("#pkType").value.split(":");
    cfg.folders = cfg.folders || []; cfg.folders.push({ path, type: t, library_id: +lid });
    $("#picker").hidden = true;
    $("#folderMsg").textContent = g.message;
    saveCfg();
  });

  async function loadSettings() {
    const s = await api("settings");
    const f = $("#setForm");
    for (const [k, v] of Object.entries(s)) { const el = f.elements[k]; if (!el) continue; if (el.type === "checkbox") el.checked = !!v; else el.value = v ?? ""; }
    const st = await api("status"); const md = st.metadata || {};
    $("#metaState").textContent = md.enabled ? (md.running ? `läuft ${md.done}/${md.total}` : `${md.found} gefunden, ${md.not_found} nicht gefunden`) + (md.last_error ? " – " + md.last_error : "") : "";
  }
  $("#setForm").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = ev.target, body = {};
    for (const el of f.elements) { if (!el.name) continue; body[el.name] = el.type === "checkbox" ? el.checked : (el.type === "number" ? parseInt(el.value || "0", 10) : el.value); }
    try { const r = await api("settings", { method: "PUT", body: JSON.stringify(body) }); $("#setMsg").textContent = r.message; me = await api("me"); }
    catch (e) { $("#setMsg").textContent = "Fehler: " + e.message; }
  });

  $("#metaRefresh").addEventListener("click", async () => { await api("meta", { method: "POST", body: JSON.stringify({ action: "refresh", id: 0 }) }); $("#metaState").textContent = "Wird neu geladen…"; });

  async function loadLog() { const r = await api("log"); $("#logBox").textContent = (r.lines || []).slice().reverse().join("\n"); $("#logBox").scrollTop = 0; } // neueste oben
  $("#reloadLog").addEventListener("click", loadLog);

  async function start() {
    if (!sid) { showLogin(); return; }
    try { me = await api("me"); }
    catch (e) { if (e.message !== "Nicht angemeldet") showLogin(e.message); return; }
    $("#login").hidden = true; $("#tabs").hidden = false;
    $("#who").textContent = me.account + (me.admin ? " (Admin)" : "");
    $("#logout").hidden = false;
    $$("[data-admin]").forEach((b) => (b.hidden = !me.admin));
    $$("[data-dsm]").forEach((b) => (b.hidden = !me.dsm_admin));
    buildKindTabs();
    showTab("library");
  }
  start();
  setInterval(() => { if (!$('[data-panel="status"]').hidden) loadStatus().catch(() => {}); }, 15000);
})();
