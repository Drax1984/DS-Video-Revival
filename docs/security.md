# Sicherheit

- **Keine Root-Rechte:** `conf/privilege` → `run-as: package`. Keine DSM-Systemdatei
  wird verändert, keine Firewall-Regel gesetzt, keine Signaturprüfung umgangen.
- **Passwörter:** Im DSM-Modus prüft DSM selbst (inkl. 2FA, Kontosperre). DS Video
  Revival reicht die Anmeldung nur durch und speichert keine Passwörter.
  Lokaler Modus (nur Tests): PBKDF2-SHA256, 210 000 Iterationen.
- **Sitzungen:** DSM-sid wird unverändert genutzt und regelmäßig gegen DSM
  geprüft (`SYNO.FileStation.Info`); DSM-Logout/-Ablauf beendet auch hier die
  Sitzung. Auf Platte nur SHA-256 der sid.
- **Brute-Force:** eigene Sperre (5 Fehlversuche / 10 min je IP), zusätzlich zur
  DSM-Sperre. Grund: DSM sieht alle Anmeldungen von `127.0.0.1`. Empfehlung:
  `127.0.0.1` **nicht** auf die DSM-Sperrliste geraten lassen (Zulassungsliste
  der automatischen Blockierung).
- **DSM-Sitzung im Browser:** Die Weboberfläche meldet sich über DSM mit eigener
  Sitzung an, übernimmt aber kein DSM-Cookie und schickt keins mit (ab 0.5.42) –
  Abmelden hier beendet nur diese Sitzung, nicht die DSM-Oberfläche.
- **CSRF:** Verwaltungs-API nur mit sid im Header `X-DSVR-SID`. Einzige Ausnahme:
  ein HttpOnly-Cookie (SameSite=Strict, Pfad `/dsvr/media/`) nur zum Laden von
  Bildern in der Weboberfläche. Content-Security-Policy ohne Inline-Skripte.
- **Rechte:** Freigaben je Medienordner, Altersgrenzen und Download-Recht je
  Benutzer/Gruppe (ab 0.5.0/0.5.23); Ordneransicht, Dateipfade und Löschen nur für
  DSM-Administratoren. „Nur eine Verbindung gleichzeitig“ und Trennen/Sperren von
  Verbindungen (ab 0.5.33/0.5.38); getrennte Sitzungen bleiben ungültig.
- **Streams:** an Benutzer gebunden, fremde Stream-IDs werden abgewiesen.
- **Mitschnitt-Werkzeug:** entfernt Passwörter, sids, Tokens, Kontonamen vor dem
  Schreiben.

## Bekannte Grenzen

1. Kein Rate-Limit für Streaming-Anfragen angemeldeter Benutzer.
2. HTTPS nur optional; im LAN standardmäßig HTTP (wie DSM-Port 5000). Für den
   Fernzugriff DSM über HTTPS bzw. einen Reverse-Proxy mit Zertifikat nutzen.
3. Gerätesperren („Trennen mit Sperre“) gelten bis zum nächsten Paketneustart.
