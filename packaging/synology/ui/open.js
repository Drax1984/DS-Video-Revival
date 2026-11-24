// DSM-Symbol „DS Video Revival“: Weboberfläche unter derselben Adresse wie DSM
// öffnen (…/dsvr/, von DSMs Webserver an das Paket weitergereicht). So klappt
// es auch von unterwegs über QuickConnect, DynDNS oder eine eigene Domain.
// Antwortet das Paket dort nicht (z.B. Weiterleitung in DSM fehlt), direkt
// über den Port der Weboberfläche wie in früheren Versionen.
(function () {
  var port = "5180"; // Port der Weboberfläche – passen postinst und „Einstellungen“ an
  var host = location.hostname.indexOf(":") >= 0 ? "[" + location.hostname + "]" : location.hostname;
  var direct = "http://" + host + ":" + port + "/dsvr/";
  var a = document.getElementById("direct");
  if (a) a.href = direct;
  function go(u) { location.replace(u); }
  if (!window.fetch) { go("/dsvr/"); return; }
  fetch("/dsvr/api/health", { cache: "no-store", credentials: "omit" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (j) { go(j && j.ok ? "/dsvr/" : direct); })
    .catch(function () { go(direct); });
})();
