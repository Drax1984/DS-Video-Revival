"use strict";
// Sprache der Weboberfläche: Deutsch (Quelltexte) oder Englisch (Wörterbuch
// lang-en.js). Auswahl je Browser: automatisch (Sprache von Browser bzw.
// Betriebssystem), Deutsch oder English.
(function () {
  let pref = "auto";
  try { pref = localStorage.getItem("dsvr_lang") || "auto"; } catch (e) { /* kein Speicher */ }
  const sys = ((navigator.languages && navigator.languages[0]) || navigator.language || "de").toLowerCase();
  const lang = pref === "de" || pref === "en" ? pref : (sys.startsWith("de") ? "de" : "en");
  document.documentElement.lang = lang;

  const norm = (s) => String(s).replace(/\s+/g, " ").trim();
  const dict = {};
  const patterns = []; // [RegExp, Vorlage] für Texte mit Platzhaltern {0}, {1} …

  function add(map) {
    for (const [k, v] of Object.entries(map)) {
      const key = norm(k);
      dict[key] = v;
      if (/\{\d\}/.test(key)) {
        const re = new RegExp("^" + key.replace(/[.*+?^$()|[\]\\]/g, "\\$&").replace(/\\?\{(\d)\\?\}/g, "(.+?)") + "$");
        const order = [...key.matchAll(/\{(\d)\}/g)].map((m) => +m[1]);
        patterns.push([re, v, order]);
      }
    }
  }

  // t("Text {0}", wert): übersetzt (falls Englisch) und setzt Werte ein. Ohne
  // Werte werden auch zusammengesetzte Texte (z.B. Meldungen des Servers)
  // über die Platzhalter-Vorlagen erkannt.
  function t(s, ...args) {
    if (s == null) return "";
    let out = String(s);
    if (lang === "en") {
      const k = norm(out);
      if (Object.prototype.hasOwnProperty.call(dict, k)) out = dict[k];
      else if (!args.length) {
        for (const [re, tpl, order] of patterns) {
          const m = re.exec(k);
          if (m) {
            const vals = []; order.forEach((n, i) => { vals[n] = t(m[i + 1]); });
            out = tpl.replace(/\{(\d)\}/g, (_, i) => vals[+i] ?? "");
            break;
          }
        }
      }
    }
    return args.length ? out.replace(/\{(\d)\}/g, (_, i) => (args[+i] ?? "")) : out;
  }
  // tc("liste", "Benutzer"): gleiches deutsches Wort, im Englischen je nach
  // Zusammenhang anders („Users“ statt „User“) – Schlüssel „liste:Benutzer“.
  function tc(ctx, s, ...args) {
    const k = ctx + ":" + norm(s);
    if (lang === "en" && Object.prototype.hasOwnProperty.call(dict, k)) return t(dict[k], ...args);
    return t(s, ...args);
  }

  // Statische Texte der Seite (Textknoten und Attribute) übersetzen.
  const ATTRS = ["placeholder", "title", "aria-label", "alt"];
  function apply(root) {
    if (lang !== "en") return;
    const skip = (el) => !el || /^(SCRIPT|STYLE|CODE|PRE)$/.test(el.tagName) || !!el.closest("[translate=no]");
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode: (n) => (skip(n.parentElement) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT),
    });
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const n of nodes) {
      const k = norm(n.nodeValue);
      if (!k || !Object.prototype.hasOwnProperty.call(dict, k)) continue;
      const lead = /^\s/.test(n.nodeValue) ? " " : "", trail = /\s$/.test(n.nodeValue) ? " " : "";
      n.nodeValue = lead + dict[k] + trail;
    }
    for (const el of root.querySelectorAll("*")) {
      if (el.closest("[translate=no]")) continue;
      for (const a of ATTRS) {
        const v = el.getAttribute(a);
        if (v && Object.prototype.hasOwnProperty.call(dict, norm(v))) el.setAttribute(a, dict[norm(v)]);
      }
    }
  }

  window.I18N = {
    lang, pref, t, tc, add, apply,
    locale: lang === "de" ? "de-DE" : "en-GB",
    setPref(p) { try { localStorage.setItem("dsvr_lang", p); } catch (e) { /* kein Speicher */ } },
  };
})();
