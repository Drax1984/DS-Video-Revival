# Jellyfin-Apps (ab 0.6.6)

DS Video Revival kann sich zusätzlich wie ein Jellyfin-Server verhalten. Damit
laufen Jellyfin-Apps mit derselben Mediathek – als zweites Standbein, falls die
DS-video-Apps einmal aus App Store oder Play Store verschwinden oder auf neuen
Geräten nicht mehr starten.

Gleich wie in den DS-video-Apps: Bibliotheken (auch eigene), Benutzerrechte
(Ordnerfreigaben, FSK, Herunterladen, „Nur eine Verbindung gleichzeitig“),
Wiedergabepositionen, „Zuletzt gesehen“. Eine in Infuse angefangene Folge lässt
sich in DS video fortsetzen und umgekehrt.

## Geeignete Apps

| App | Geräte |
|---|---|
| Infuse | iPhone, iPad, Apple TV, Mac |
| Swiftfin | iPhone, iPad, Apple TV |
| Jellyfin für Android TV | Android TV, Google TV, Fire TV |
| Findroid, Streamyfin | Android-Smartphone und -Tablet |
| Jellyfin (offizielle App) | iPhone, Android-Smartphone – mit eigener, einfacher Mediathek (ab 0.6.6) |

**Offizielle App „Jellyfin“ (iPhone, Android):** Sie zeigt nur die
Weboberfläche des Servers an. Die Jellyfin-Weboberfläche ist nicht enthalten;
stattdessen liefert DS Video Revival unter `/web/` eine eigene, einfache
Mediathek. Dazu gehören eine Startseite mit „Weiterschauen“, „Als Nächstes“ und
den neuen Titeln je Bibliothek, außerdem Bibliotheken mit Sortierung, Suche,
eine Detailansicht mit Staffeln und Folgen sowie die Wiedergabe direkt oder
umgewandelt mit Untertiteln. Die Anmeldung läuft mit Passwort oder Quick
Connect. Mehr Komfort, etwa Tonspurwahl, Download und native Wiedergabe,
bieten Swiftfin (kostenlos) oder Infuse.

Nicht enthalten sind Musik, Live-TV, Fernsteuerung zwischen Geräten (SyncPlay,
Cast) und Verwaltungsfunktionen.

Die Schnittstelle meldet sich als Jellyfin 12.3. Erprobt ist sie bisher mit
automatischen Tests gegen die Datenmodelle der Jellyfin-SDK. Rückmeldungen zu
einzelnen Apps sind willkommen.

## Einschalten

1. Weboberfläche → **Einstellungen** → **Jellyfin-Schnittstelle** →
   „Jellyfin-Schnittstelle einschalten“ → Speichern. Ein Neustart ist nicht
   nötig.
2. Port: Standard **8096**, wie bei Jellyfin. Läuft auf der NAS schon ein
   echter Jellyfin-Server, einen anderen Port wählen, z.B. 8097.
3. Falls die DSM-Firewall aktiv ist: TCP 8096 (bzw. der gewählte Port) und für
   die automatische Suche UDP 7359 im Heimnetz erlauben.

Danach erscheint für alle Benutzer der Reiter **Jellyfin-Apps** mit den
Adressen, Quick Connect und den angemeldeten Geräten.

## Adresse in der App

- **Im Heimnetz:** `http://NAS-IP:8096`. Infuse und Jellyfin für Android TV
  finden den Server meist von selbst.
- **Über DSM:** `https://NAS-IP:5001/jellyfin` bzw. mit der eigenen
  DSM-Domain `https://nas.example.com:5001/jellyfin`. DSM reicht die Aufrufe
  an das Paket weiter, mit DSM-Zertifikat. Das funktioniert auch über einen
  Reverse-Proxy vor DSM.
- Eine Synology-QuickConnect-ID funktioniert in Jellyfin-Apps nicht.

Den Port 8096 nicht per Portweiterleitung ins Internet öffnen. Für unterwegs
die Adresse über DSM mit HTTPS oder ein VPN nutzen.

## Anmelden

- **DSM-Konto und Passwort:** Das Paket prüft die Anmeldung kurz bei DSM
  (Passwort, Kontosperre, Anwendungsberechtigung „DS Video Revival“) und meldet
  diese DSM-Sitzung gleich wieder ab. Das Passwort wird nicht gespeichert.
- **Quick Connect:** In der App „Quick Connect“ wählen (in der offiziellen App
  „Jellyfin“: „Mit Quick Connect anmelden“). Die App zeigt einen
  sechsstelligen Code, den man in der Weboberfläche unter **Jellyfin-Apps**
  bestätigt. Das ist der Weg für Konten mit **Zwei-Faktor-Anmeldung** – die
  Jellyfin-Apps können keinen 2FA-Code abfragen – und bequem auf dem Fernseher.
- Die App bekommt ein eigenes Zugriffstoken. Es bleibt gültig, bis man sich in
  der App abmeldet, es unter **Jellyfin-Apps → Angemeldete Geräte** bzw.
  **Verbindungen → Trennen** abmeldet oder das Gerät 180 Tage nicht da war.
  Wer in DSM die Anwendungsberechtigung verliert, kann sich nicht neu
  anmelden. Ein schon angemeldetes Gerät deshalb zusätzlich abmelden.

## Wiedergabe

- **Direkt:** Kann die App die Datei selbst abspielen (Infuse, Swiftfin mit
  VLC und Findroid können fast alles), liefert das Paket die Originaldatei.
- **Umgewandelt:** Sonst wandelt FFmpeg wie bei DS video nach HLS um.
  Bei H.264/HEVC wird das Video nur umverpackt, andere Formate werden nach
  H.264 umgewandelt. Gesteuert wird das über das Geräteprofil, das die App
  mitschickt. Fortsetzen und Springen funktionieren.
- H.264 mit 10 Bit wird immer umgewandelt, weil es die Geräte nicht abspielen können.
- **App „Jellyfin“ für Android:** Filme spielt der Player der App (ExoPlayer) direkt aus der Datei, auch MKV
  und 4K. Wer unter „Menü“ → „App-Einstellungen“ den Web-Player wählt, nutzt den Player der Mediathek; mit
  „Externer Player“ startet die App z.B. VLC mit der Datei (Position wird beim Beenden übernommen). Filme über
  1080p oder mit HEVC 10 Bit/HDR wandelt der Server dafür wie für die App „DS video“ in H.264 (höchstens 1080p) um. Das
  Zahnrad im Player ist die Qualitätswahl der App: Voreingestellt ist „Automatisch“ (ganz unten in der Liste),
  also Originalqualität, solange die Leitung reicht, sonst wandelt der Server passend um.
- **Mediathek der App „Jellyfin“:** Startet ein Film nicht, versucht sie es von selbst noch einmal, zuerst mit
  einem eigenen Player (hls.js), dann mit umgewandeltem Ton, zuletzt vollständig umgewandelt.
- **Untertitel:** Eingebettete Untertitel und SRT/ASS/VTT-Dateien neben dem
  Film. Bei der Umwandlung werden Textuntertitel getrennt geliefert, also nicht
  ins Bild eingebrannt. Bilduntertitel (PGS, VobSub) gibt es nur bei direkter
  Wiedergabe.
- Herunterladen in der App (z.B. Findroid, Swiftfin) nur mit dem Recht
  „Herunterladen“. In der Mediathek der App „Jellyfin“ gibt es dafür
  „⤓ Herunterladen“. Die Datei landet in den Downloads der App: auf dem iPhone im
  Reiter „Downloads“, unter Android über „Menü“ → „Downloads“ in der Mediathek
  (Ansicht der App mit Plakat und Offline-Wiedergabe). Die Android-App lädt voreingestellt
  nur im WLAN und zeigt den Fortschritt auch als Benachrichtigung. Auf dem iPhone
  bereitet der Server Dateien, die nicht MP4 sind, vorher als MP4 vor (H.264/HEVC
  nur umverpackt). Die Mediathek zeigt Vorbereitung und Übertragung mit Restzeit.
  Was danach in der App liegt, sieht sie nicht (Löschen in der App erfährt sie
  nicht), daher gibt es keine Markierung „heruntergeladen“.
- **Unterwegs:** Die Mediathek der App „Jellyfin“ misst vor dem Abspielen die
  Leitung. Ist sie zu langsam für die Datei, wandelt der Server mit passender
  Bitrate um. Das hängt meist am Upload des Heimanschlusses.

## Verbindungen und Sicherheit

- Jellyfin-Apps erscheinen unter **Verbindungen** als „Jellyfin-App“, mit App
  und Gerätename. „Trennen“ meldet das Gerät ab, auf Wunsch mit Sperre.
- Tokens liegen nur als SHA-256 in `var/jellyfin.json`, ebenso die Favoriten
  und die Ansichtseinstellungen der Apps.
- Bilder laden die Apps wie bei Jellyfin ohne Anmeldung. Das klappt nur mit
  einem fälschungssicheren Bild-Tag aus einer angemeldeten Abfrage, ohne Tag
  nur mit Token.
- Anmeldeversuche zählen zur selben Sperre wie die DS-video-Apps: 5
  Fehlversuche in 10 Minuten je Adresse, dazu DSMs eigene Sperre.

## Fehlersuche

| Symptom | Ursache / Lösung |
|---|---|
| App findet keinen Server | Schnittstelle eingeschaltet? Adresse mit Port eingeben, z.B. `http://192.168.1.10:8096`. Ist die Firewall aktiv, TCP 8096 bzw. UDP 7359 erlauben |
| „Server-Version nicht unterstützt“ | Rückmeldung mit App-Version erbeten (Protokoll: Zeilen mit `jellyfin:`) |
| Anmeldung schlägt fehl, Konto hat 2FA | Quick Connect verwenden |
| Etwas fehlt oder bricht ab | Protokoll: `jellyfin: nicht umgesetzt: …` zeigt Aufrufe der App, die das Paket nicht kennt. Bitte zurückmelden |
| Port belegt | Läuft auf der NAS ein echter Jellyfin? Anderen Port wählen, z.B. 8097 |
