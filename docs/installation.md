# Installation (DSM 7.x)

> Testversion. Erst auf einer Test-NAS oder in Virtual DSM ausprobieren.

## 1. Paket installieren

1. Paket-Zentrum → **Manuelle Installation** → `dsvideorevival-<version>.spk`
2. Hinweis „Paket eines Drittanbieters“ bestätigen (das Paket ist nicht von
   Synology signiert).
3. Assistent: Port (Standard **5180**), DSM-Adresse (Standard
   `http://127.0.0.1:5000`, nur ändern, wenn der DSM-HTTP-Port geändert wurde),
   Medienordner (`movie:/volume1/video/Filme;tvshow:/volume1/video/Serien`).

## 2. Leserechte vergeben (einmalig, Pflicht)

DSM 7 führt Drittanbieterpakete unter einem eigenen Systembenutzer aus.
Systemsteuerung → Freigegebener Ordner → *video* → Bearbeiten → Berechtigungen →
Dropdown **„Systeminterner Benutzer“** → `dsvideorevival` → **Nur Lesen**.

Danach in der Verwaltung (Hauptmenü → *DS Video Revival*) „Bibliothek jetzt
einlesen“. Nicht lesbare Ordner werden dort rot angezeigt.

## 3. App verbinden

DS-Video-App → Adresse: `NAS-IP:5180` (bzw. gewählter Port), DSM-Konto und Passwort.
Nach Umstellung auf die Standardports (3c) genügt `NAS-IP` bzw. die DynDNS-Adresse.

## 3a. Poster, Inhalte, IMDb

Funktioniert ohne Schlüssel: Inhalte und Plakate kommen aus Wikidata/Wikipedia,
Serien aus TVmaze, IMDb-Bewertungen aus den offiziellen IMDb-Datensätzen. Die NAS
braucht dafür Internetzugang. Falsche Treffer: Titel öffnen → „Metadaten
korrigieren“ → IMDb-ID (`tt…`) eintragen. Optional ein TMDb-Schlüssel
(Einstellungen) für mehr Plakate und Hintergrundbilder. Ab 0.5.35 wird eine
eingetragene IMDb-ID mit TMDb-Schlüssel über TMDb aufgelöst (Plakat, Hintergrund,
deutsche Texte; Wikidata nur als Rückfall); ohne Schlüssel kommt das Plakat aus
der englischen Wikipedia, ersatzweise aus Wikimedia Commons (Filmplakat).
Die Zuordnung funktioniert auch für Serien. Ab 0.5.36 werden Plakate auf Gültigkeit
geprüft: Ein fehlendes, leeres oder kaputtes lokales Plakat (`poster.jpg`,
`folder.jpg`, `<Film>.jpg` … – z.B. nach Verschieben des Ordners) fällt auf das
Plakat aus den Metadaten zurück; liefert TMDb in der Detailabfrage kein Plakat,
wird eines aus der Bilderliste des Titels genommen.

## 3b. FFmpeg für MKV/AVI/AC3/DTS

Paket-Zentrum → Einstellungen → Paketquellen → `https://packages.synocommunity.com` hinzufügen →
Paket **ffmpeg7** installieren. DS Video Revival findet es automatisch.

## 3c. Verbindung ohne Port (wie früher Video Station)

Ab 0.4.0 richtet das Paket bei der Installation eine Weiterleitung in **DSMs
eigenem Webserver** ein (DSM-Ressourcen-Worker `web-config`, Typ `dsm`, ab DSM
7.2). DSM bleibt auf 5000/5001 mit seinem Zertifikat; nur die DS-video-Apps
werden an das Paket gereicht:

- `query.cgi` mit User-Agent `Synology-DS_video…` → Paket (es meldet die
  Video-Station-APIs unter `/webapi/_dsvr/…`),
- alles unter `/webapi/_dsvr/` → Paket (Port 5180, intern).

Anmeldung, Zertifikat, Router, Reverse-Proxy (z.B. Nginx Proxy Manager →
DSM) bleiben wie bisher. In DS video genügt die gleiche Adresse wie in DS file.
Prüfen: Verwaltung → Einstellungen → „Verbindung für DS video“.

Der Port-Modus aus 0.3.x (DSM auf 5010/5011) entfällt; ist er noch aktiv, zeigt
die Verwaltung einen Knopf zum Zurückstellen.

## 4. Fernzugriff (sicher)

DS video nutzt ohne Portangabe dieselben Ports wie DSM (5000 HTTP / 5001
HTTPS; belegt im App-Code `composeValidURL(adresse, https, 5000, 5001)`). Alles,
was für DS file von außen funktioniert, funktioniert damit auch für DS video.

- **QuickConnect** funktioniert: in der App die QuickConnect-ID eintragen. DSM
  reicht die Aufrufe wie bei DS file an das Paket weiter.
- **Eigene Domain mit HTTPS**: Portweiterleitung bzw. Reverse-Proxy (z.B. Nginx
  Proxy Manager) auf DSM (5001 bzw. 5000) – in der App die Domain ohne Port,
  HTTPS an.
- **VPN** (WireGuard/OpenVPN auf Router oder NAS): wie im LAN die NAS-IP ohne Port.

Den internen Port 5180 nicht per Portweiterleitung ins Internet öffnen – er
wird von außen nicht gebraucht.

**Vorgeschalteter Reverse-Proxy (z.B. Nginx Proxy Manager):** Damit lange
Filme nicht nach einigen Minuten abbrechen („unexpected end of stream“, auf dem
Fernseher „Video kann nicht abgespielt werden“), beim Proxy-Host unter
„Advanced → Custom Nginx Configuration“ eintragen:

```
proxy_buffering off;
proxy_request_buffering off;
proxy_read_timeout 3600s;
proxy_send_timeout 3600s;
send_timeout 3600s;
```

Grund: nginx bricht eine Antwort ab, wenn der Empfänger 60 s lang nichts liest
(`send_timeout`). Player mit großem Puffer tun genau das; mit Pufferung liest
der Proxy zudem bis zu 1 GB voraus und wartet dann. DSMs eigener Webserver ist
ab 0.5.28 im Paket entsprechend eingestellt.

## 5. Firewall

Die DSM-Firewall wird **nicht** automatisch geändert. Der Dienst
„DS Video Revival“ ist in der Firewall-Dienstliste auswählbar.

## Dateien

| Pfad | Inhalt |
|---|---|
| `/var/packages/dsvideorevival/var/config.json` | Konfiguration |
| `…/var/library.json` | Bibliotheksindex und Wiedergabepositionen |
| `…/var/sessions.json` | Sitzungen (nur SHA-256-Hashes der sids) |
| `…/var/unhandled.json` | nicht implementierte Client-Aufrufe (nur Parameternamen) |
| `…/var/dsvr.log` | Protokoll (rotiert bei 5 MB) |
| `…/var/offline/` | Offline-Umwandlungen (MP4-Dateien, werden nach 7 Tagen gelöscht) |

## Update / Rollback / Deinstallation

- Update: neues `.spk` manuell installieren; `var/` bleibt erhalten.
- DSM-Updates (Firmware der Synology) wie gewohnt einspielen: DS Video Revival
  muss dafür weder deinstalliert noch neu installiert werden; das Paket startet
  danach von selbst, die Weiterleitung für die Apps bleibt bestehen.
- Rollback: älteres `.spk` installieren (Indexformat ist versioniert; bei
  Inkompatibilität wird der Index gesichert und neu eingelesen).
- Deinstallation entfernt Paket und `var/`. Mediendateien werden nie verändert.

## Benutzerrechte (ab 0.5.0)

**Wer sich anmelden darf** (App und Weboberfläche): DSM → Systemsteuerung →
Anwendungsberechtigungen → „DS Video Revival“ → Bearbeiten. Das Paket fragt
bei jeder Anmeldung (und danach alle 15 Minuten) bei DSM nach, ob der Benutzer
die Berechtigung hat (`SYNO.Core.Desktop.Initdata` → `AppPrivilege`, nicht
offiziell dokumentiert). Ohne Berechtigung wird die Anmeldung mit Code 402
(„Permission denied“) abgewiesen. Liefert DSM die Angabe nicht, wird nicht
abgewiesen, sondern im Protokoll vermerkt („Anwendungsberechtigung nicht prüfbar“).

**Aussperrschutz:** DSM-Administratoren werden nie abgewiesen und sehen immer
alle Inhalte (Freigaben und Altersgrenzen gelten für sie nicht).

**Wer was sieht** (Weboberfläche → Benutzerrechte, nur Administratoren):
- Eigene Gruppen: aus den DSM-Benutzern zusammenstellen, bearbeiten, löschen
  (unabhängig von den DSM-Gruppen; nutzbar in Freigaben und Altersgrenzen).
- Medienordner freigeben: ohne Auswahl sehen alle den Ordner, sonst nur die
  angehakten Benutzer/Gruppen (lokale DSM-Gruppen laut `/etc/group`).
- Jugendschutz: Altersgrenze FSK 0/6/12/16/18 je Benutzer oder Gruppe, optional
  „Titel ohne FSK-Angabe sperren“. Benutzerregel vor Gruppenregel, sonst die
  strengste Gruppenregel. Ist das Konto unbekannt, gilt die strengste Regel.
- Die FSK stammt aus den Metadaten und lässt sich pro Titel in der
  Detailansicht von Hand setzen. Serienfolgen erben die FSK der Serie.
- Die Filter gelten serverseitig für App und Weboberfläche (Listen, Details,
  Bilder, Wiedergabe).

## Titel umbenennen und löschen (ab 0.5.15, nur Administratoren)

- **Titel:** Mediathek → Titel öffnen → „Titel“. Der Name gilt in der Weboberfläche und in den DS-video-Apps (Anzeige und Sortierung) und bleibt bei neuen Scans erhalten. „Automatisch“ stellt den Namen aus Dateiname bzw. Metadaten wieder her.
- **Löschen:** Mediathek → Film/Heimvideo öffnen → „Titel löschen“. Ist für den freigegebenen Ordner der Papierkorb aktiv, werden die Videodateien dorthin verschoben, sonst endgültig gelöscht (die Oberfläche fragt vorher nach). Begleitdateien (Untertitel, Poster, .nfo) und der Ordner bleiben liegen.
- Zum Löschen braucht der Paketbenutzer `dsvideorevival` **Lesen/Schreiben** auf dem freigegebenen Ordner. Fehlt es, bietet die Oberfläche an, es zu setzen (andere Rechte bleiben unverändert), oder von Hand: Systemsteuerung → Freigegebener Ordner → Bearbeiten → Berechtigungen → „Systeminterner Benutzer“.
- Ordner und Dateien mit `#` oder `!` am Anfang werden eingelesen; ausgelassen werden nur Synology-Systemordner (`#recycle`, `#snapshot`, `@…`) und versteckte Ordner.

## Bibliotheken und TV-Aufnahmen (ab 0.5.16)

- **Standardbibliotheken:** Filme, Serien, Heimvideos und TV-Aufnahmen. Jeder Medienordner gehört genau einer Bibliothek an (Medienordner → Spalte „Bibliothek“).
- **Eigene Bibliotheken** (z.B. „Dokus“, „Kinderfilme“): Medienordner → „Eigene Bibliotheken“ → Name und Typ eingeben → „Bibliothek anlegen“, danach Ordner zuordnen. Sie erscheinen als eigener Reiter in der Mediathek und als eigener Bereich in den DS-video-Apps. Beim Löschen einer Bibliothek kommen ihre Ordner in die Standardbibliothek des Typs zurück; Dateien bleiben unberührt.
- **TV-Aufnahmen:** Ordner mit dem Typ „TV-Aufnahmen“. Bei Enigma2-Aufnahmen (VU+, Gigablue …, Dateiname `JJJJMMTT HHMM - Sender - Titel.ts`) werden Sender, Sendung und Aufnahmezeit erkannt; in den Apps gibt es dann auch die Ansichten nach Sender und nach Sendung.
- Wird ein Medienordner entfernt oder einer anderen Bibliothek zugeordnet, verschwinden bzw. wandern seine Titel beim anschließenden Einlesen (gespeicherte Wiedergabepositionen dieser Dateien gehen dabei verloren).

## Gruppen und Administratoren von DS Video Revival (ab 0.5.19)

- **Eigene Gruppen** (Benutzerrechte → Gruppen) gelten nur in DS Video Revival (Weboberfläche und DS-video-Apps), nie in DSM. Anlegen, Bearbeiten (Name, Mitglieder) und Löschen werden sofort gespeichert.
- **Administratoren von DS Video Revival** (eigener Abschnitt unter Benutzerrechte, nicht löschbar): Die Mitglieder haben in DS Video Revival alle Admin-Funktionen (Mediathek verwalten, Titel umbenennen/löschen, Benutzerrechte, Einstellungen) und sehen alle Inhalte, bekommen aber keine Rechte in DSM. DSM-Administratoren gehören immer dazu (Aussperrschutz). Funktionen, die DSM-Rechte brauchen (z.B. Lese-/Schreibrecht auf einen freigegebenen Ordner setzen), funktionieren weiterhin nur mit einem DSM-Administrator; sonst nennt die Oberfläche den Weg von Hand.
- DSM-Gruppen (z.B. administrators, users, video) werden hier nicht bearbeitet – eine Änderung würde auch die DSM-Rechte ändern. Sie lassen sich weiterhin bei Ordnerfreigaben und Altersgrenzen auswählen.
- Änderungen an den Administratoren stehen im Protokoll.

## Offline-Download und Offline-Umwandlung (ab 0.5.21)

- Dateien, die die App direkt abspielen kann (MP4/MOV mit H.264 und AAC-LC), lädt sie im Original herunter.
- Alle anderen (z.B. MKV, E-AC-3/AC-3/DTS-Ton, HEVC, Hi10) wandelt das Paket wie früher Video Station per **Offline-Umwandlung** in eine MP4-Datei (H.264 + AAC-LC) um; die App lädt sie nach Abschluss herunter. Voraussetzung: FFmpeg mit AAC-Encoder (Paket „ffmpeg7“); für Videoumwandlung zusätzlich ein H.264-Encoder.
- Qualitätsstufen (eigene Festlegung): **Hoch** bis 1080p (H.264 bis 1080p wird nur kopiert – schnell, nur der Ton wird umgewandelt), **Mittel** bis 720p/3 Mbit/s, **Niedrig** bis 480p/1,2 Mbit/s.
- Es läuft immer nur eine Umwandlung; weitere warten. Fertige Dateien liegen in `var/offline/` und werden nach 7 Tagen gelöscht. (Die App entfernt die Aufgabe schon vor dem Herunterladen; die Datei bleibt deshalb bis dahin abrufbar.)
- Protokoll: Zeilen mit `offline:` zeigen Anlegen, Start (kopiert/umgewandelt), Fertigstellung und Fehler.
- **Wer herunterladen darf** (ab 0.5.23): Benutzerrechte → Herunterladen („Alle“ oder ausgewählte Benutzer/Gruppen; Administratoren immer). Für alle anderen lehnt das Paket Download und Offline-Umwandlung ab und meldet der App die Offline-Umwandlung als nicht erlaubt. Den Download-Knopf blendet die App selbst nicht aus – der Download schlägt dann fehl (Protokoll: `download: Konto … darf nicht herunterladen`).


## Ordneransicht und Dateipfade (ab 0.5.24, nur DSM-Administratoren)

- Reiter **Ordneransicht**: alle Medien nach Medienordner und Unterordner gruppiert, mit Plakat, Titel und vollständigem Dateipfad; Suche nach Titel, Datei oder Ordner. Dateipfad anklicken öffnet den Ordner in der File Station (neues Fenster, DSM-Anmeldung nötig), „Ordnerpfad kopieren“ kopiert den Ordnerpfad.
- **Mediathek**: Jeder Titel hat oben rechts einen kleinen „Info“-Knopf; er zeigt den Dateipfad (bei Serien den Serienordner), Anklicken kopiert ihn.
- Ab 0.5.30 in der Ordneransicht: Filter nach Medienart (Alles, Filme, Serien, Heimvideos, TV-Aufnahmen), Ansicht „Nur Doppelgänger“ (gleicher Film laut TMDb, gleiche Folge oder mehrere Dateien desselben Titels, z.B. „Sample“ – mit Auflösung, Codecs, Laufzeit und Größe zum Vergleich) und „Löschen“ je Datei (Papierkorb der Freigabe, falls aktiv, sonst endgültig).
- Ab 0.5.31: Darf das Paket in einem Ordner nicht schreiben (z.B. von einem Download-Programm angelegte Unterordner wie „Sample“ mit eigenen Rechten), löscht es die Datei über DSM mit den Rechten des angemeldeten Administrators – wie in der File Station (SYNO.FileStation.Delete). Protokoll: `über DSM (File Station) gelöscht`.
- Ab 0.5.32: „Alle Doppelgänger löschen“ (je Titel bleibt die beste Datei: höchste Auflösung, dann längste Laufzeit, dann größte Datei – markiert mit „bleibt“); Teile mehrteiliger Filme (CD1/CD2, Part/Teil/Disc 1, 2 …) gelten nicht als Doppelgänger. Detailansicht: Auflösung und Dateigröße unter der Beschreibung.
- Ab 0.5.40: In der Ansicht „Nur Doppelgänger“ lässt sich jede Datei **ignorieren** – sie zählt dann dauerhaft nicht mehr als Doppelgänger (auch nach Neustart und erneutem Einlesen; gespeichert als Dateipfad in `config.json`, `dup_ignore`). Ignorierte Dateien stehen unten unter „Ignorierte Dateien“ und lassen sich dort mit „Wieder anzeigen“ zurückholen. „Alle Doppelgänger löschen“ lässt ignorierte Dateien unberührt.
- Ab 0.5.34: DVD-Ordner (`…/DISC_3/VIDEO_TS/VTS_01_2.VOB`) – jede VOB-Datei ist ein eigener Abschnitt ihrer Disc; verschiedene Discs bzw. Abschnitte gelten nicht als Doppelgänger, dieselbe Disc an zweiter Stelle derselben Serie schon. Teile wie „1von3“, „2 von 3“, „1of2“ zählen als Teile. Folgen ohne erkannte Folgennummer (S00E00) werden nicht miteinander verglichen.
- Beides sehen nur DSM-Administratoren – nicht die Mitglieder der Administratorengruppe von DS Video Revival und keine normalen Benutzer.

## Ansicht „Nach Ordner“ in den Apps (ab 0.5.25)

- Oben stehen die Medienordner der Bibliothek. Ein Ordner zeigt beim Antippen alle Medien darin samt Unterordnern als eine Liste – ohne Durchklicken, mit Titel und Plakat wie unter „Alle“ (Folgen als „Serie – S01E02“).
- Ab 0.5.26 zeigen die Ordnerkacheln statt des Ordnersymbols bis zu 4 Plakate aus dem Ordner (zufällig gewählt, bei jedem Öffnen bzw. Aktualisieren andere; Serien je einmal). Ordner ohne Plakate behalten das Symbol.

## „Zuletzt gesehen“ in den Apps (ab 0.5.38)

- Zeigt nur Titel, die das angemeldete Konto angesehen hat, zuletzt gesehene zuerst – Filme, Heimvideos, TV-Aufnahmen und Serien (Serien mit angesehenen Folgen). Vorher erschienen dort alle Titel.
- „Zuletzt hinzugefügt“ bei Serien sortiert nach der neuesten Folge.

## Nur eine Verbindung gleichzeitig und Verbindungen (ab 0.5.33)

- **Benutzerrechte → Nur eine Verbindung gleichzeitig:** ausgewählte Benutzer bzw. Gruppen können mit ihrem Konto nicht auf mehreren Geräten gleichzeitig schauen. Eine Verbindung ist eine Anmeldung – je App bzw. Browser eine; die Weboberfläche zählt mit (Mediathek). Administratoren sind ausgenommen.
- Die erste aktive Verbindung hat Vorrang. Jede weitere sieht in der App leere Listen und kann nichts abspielen oder herunterladen; die Weboberfläche zeigt einen Hinweis. Frei wird der Platz, wenn auf dem ersten Gerät 5 Minuten lang nichts mehr läuft (auch keine Wiedergabe), es sich abmeldet oder getrennt wird. Danach in der App einfach die Liste aktualisieren. Eine pausierte Wiedergabe kann nach 5 Minuten den Platz verlieren – läuft dann auf einem anderen Gerät etwas, setzt sie nicht mehr fort.
- **Verbindungen** (Reiter, nur Administratoren): Konto, Gerät (aus der App- bzw. Browserkennung, z.B. „Android TV · <Modell>“, „iOS“, „Browser · Chrome (Windows)“), Adresse, Zustand (spielt ab, aktiv, ruhend, gesperrt) und zuletzt gestarteter Titel aller Verbindungen der letzten 30 Minuten; aktualisiert sich alle 10 Sekunden.
- **Trennen** meldet die Verbindung ab (die Sitzung bleibt auch nach einem Paketneustart ungültig) und bricht eine laufende Wiedergabe bzw. einen Download ab. Apps mit gespeichertem Passwort können sich danach neu anmelden – dauerhaft sperren lässt sich ein Konto in DSM unter Systemsteuerung → Anwendungsberechtigungen.
- Ab 0.5.38: **Ruhezeit** einstellbar (Benutzerrechte, 1–240 Minuten, Standard 5). **Trennen mit Sperre** (Verbindungen → „Beim Trennen zusätzlich sperren“: 15 Min. bis 24 Std.): Das Gerät – dieses Konto auf diesem Gerät und dieser Adresse – sieht bis zum Ende der Sperre keine Medien, auch nach erneuter Anmeldung; andere Geräte bleiben frei. Gesperrte Geräte stehen unter „Gesperrte Geräte“ und lassen sich vorzeitig freigeben. Sperren gelten bis zum nächsten Neustart des Pakets.
- Protokoll: `verbindungen: "konto" ist schon auf … aktiv – … sieht keine Medien`, `… – jetzt … (vorher …, ruhte seit …)` und `… hat die Verbindung von "konto" getrennt`.
