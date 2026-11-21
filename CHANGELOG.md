# Versionsverlauf

Installationspakete: [Releases](../../releases). Welches Paket für welche NAS: [README](README.md#welches-paket-für-welche-synology).

## 0.6.7 – 21.11.2026

**Schwerpunkt: App „Jellyfin“ auf iPhone und Android** – 4K/HEVC, Downloads, Android-Anbindung, weniger Last auf der NAS.

- **4K/HEVC in der App „Jellyfin“ auf dem iPhone startet sofort.** Die Mediathek lädt jetzt wie Jellyfin die
  Übersichtsliste (`master.m3u8`). Sie enthält CODECS (aus der tatsächlichen Ausgabe von FFmpeg gelesen),
  Auflösung, Bildrate und HDR-Art (`VIDEO-RANGE=PQ/HLG/SDR`). Bisher wurde die Segmentliste direkt geladen. Dann
  fragt der Apple-Player auf dem iPhone fMP4-Segmente hunderte Male ab und startet erst nach langer Zeit oder gar
  nicht. Am Mac tritt das nicht auf. Ohne HDR-Angabe meldet er bei HDR-Filmen „Media failed to decode“. Im Test
  mit iOS 18 startete 4K-HEVC mit 50 Mbit/s nach ca. 4 s, ein Sprung dauerte ca. 1,5 s.
- **App „Jellyfin“ für Android verbindet sich.** Bisher kam nach dem Laden „Es konnte keine Verbindung hergestellt
  werden“. Die Android-App erkennt die geladene Seite an einem Skript namens `main.*.bundle.js`, wie bei der
  Jellyfin-Weboberfläche, und bricht sonst nach 10 s ab. Das Skript der Mediathek heißt jetzt
  `main.dsvr.bundle.js`. Außerdem:
  - Die Zurück-Taste geht in der Mediathek zurück und schließt auf der Startseite die App.
  - Herunterladen übernimmt die App selbst, mit „Downloads öffnen“. Dafür meldet die Mediathek der App die
    Anmeldung, wie die Jellyfin-Weboberfläche.
  - **Wiedergabe mit dem Player der App** (ExoPlayer, in der App voreingestellt): Er spielt MKV, HEVC und 4K direkt
    aus der Datei und spult darin, wie die DS-video-Apps. Bisher spielte der Player der Webseite. Der musste HLS
    über JavaScript abspielen und ruckelte vor allem bei 4K. Ist in der App der Web-Player eingestellt, spielt
    weiter die Mediathek selbst.
- **Downloads in der App „Jellyfin“ (iPhone) wieder abspielbar.** Mit „experimentellen Downloads“ fragt die App den
  Server vor dem Laden selbst nach der Datei. Bisher bekam sie dabei eine HLS-Adresse und speicherte die
  Wiedergabeliste statt des Films. Beim Abspielen kam dann „This media may be damaged“ (-11829). Jetzt bekommt sie
  die fürs iPhone vorbereitete MP4 als eigene Quelle. Ohne Vorbereitung bietet der Server keine Umwandlung mehr an,
  und die App lädt die Adresse der Mediathek.
- **Herunterladen von unterwegs (Adresse über DSM, `…/jellyfin`):** Für eine direkt ladbare Datei baut die App
  die Adresse selbst ab der Wurzel des Servers und verliert dabei `/jellyfin`. Der Abruf landete bei DSM, die App
  speicherte dessen Fehlerseite (wenige KB) als Video, und die Mediathek meldete „Download nicht gestartet“. Über
  einen Basis-Pfad bekommt die App die Datei jetzt mit vollständiger Adresse. Das Laden zählt nicht als Wiedergabe
  und ändert die Position nicht.
- **Vorbereitung fürs iPhone in Originalqualität:** H.264 und HEVC (auch 4K/HDR, als „hvc1“) werden nur
  umverpackt, AAC, AC-3 und E-AC-3 bleiben. Nur was der Apple-Player nicht kann, wird umgewandelt. Das ist
  meist eine Sache von Sekunden.
- **Fortschritt beim Herunterladen:**
  - Die Vorbereitung zeigt eine Restzeit, die jede Sekunde herunterläuft und sich am tatsächlichen
    Fortschritt ausrichtet.
  - Danach zeigt die Übertragung aufs Handy Prozent und Restzeit. Der Server zählt dafür die gesendeten Bytes.
  - Am Ende steht „✓ Auf dem iPhone – unter „Downloads“ abspielbar“.
- **Keine Markierung „heruntergeladen“:** Löscht man einen Download in der App, erfährt die Mediathek das nicht.
  Eine Markierung wäre dann falsch. Der Abschluss der Übertragung wird nur einmal gemeldet.
- **Anmeldeseite der App „Jellyfin“: „Anderen Server wählen“.** Unter Android öffnet der Knopf die Serverauswahl
  der App. Auf dem iPhone bietet die App dafür keinen Aufruf, der Knopf weist auf ihren Reiter „Einstellungen“ hin.
- **Mediathek der App „Jellyfin“: Bibliotheken laden beim Scrollen weiter.** Kommt das Ende der Liste in Sicht,
  folgen die nächsten Titel von selbst. Der Knopf „Mehr laden“ entfällt.
- **Eigener Player als Rückfall:** Startet der Apple-Player nicht innerhalb von 25 s, versucht die Mediathek
  denselben Stream mit hls.js über Managed Media Source (iOS ab 17.1), erst danach die Umwandlung.
- **Weniger Last auf der NAS:** FFmpeg läuft der Wiedergabe nur noch voraus, beim Umverpacken höchstens 2 Minuten,
  beim Umwandeln 1 Minute. Danach wird es angehalten, bis der Player aufholt. Bisher wurde der ganze Film vorab
  umverpackt, bei 4K viele Gigabyte. Segmente, die mehr als 2 Minuten zurückliegen, werden gelöscht. Das machte
  die Wiedergabe im eigenen WLAN langsamer als unterwegs und bremste die NAS, auch die Anmeldung.
- Wiedergabe startet bis zu 1,5 s schneller: Der Server wartet nicht mehr pauschal auf FFmpeg.
- **Anmeldung an der Weboberfläche schneller:** Der Sitzungsname, den DSM für ein Konto akzeptiert, wird gemerkt,
  auch über einen Neustart. Das spart einen abgelehnten DSM-Anmeldeversuch. Für Administratoren entfällt die
  Wartezeit bei der Rechteprüfung. Das Protokoll nennt die Dauer der Anmeldung.
- **Mediathek in der App „Jellyfin“ (iPhone):**
  - Filme mit H.264 kommen als **MPEG-TS-Segmente**, so wie Jellyfin selbst sie an diese App liefert. Mit fMP4 lud
    der Apple-Player bei manchen Filmen (z.B. Blu-ray-Kopien mit DTS-Ton) das erste Segment endlos. HEVC kommt als
    fMP4, weil Apple HEVC nur so abspielt.
  - **Herunterladen:** Filme haben in der Detailansicht den Knopf **„⤓ Herunterladen“**, Folgen ein **⤓** (nur mit
    dem Recht „Herunterladen“). Die Datei landet im Reiter „Downloads“ der App. Weil der Player der App kein MKV
    kann, wandelt der Server solche Dateien vorher in MP4 um (wenn möglich nur kopiert).
  - **Unterwegs:** Vor dem Abspielen wird die Leitung zum Server gemessen (`/Playback/BitrateTest`, wie Jellyfin).
    Ist sie langsamer als die Datei, wandelt der Server mit passender Bitrate um.
  - Lädt ein Film länger als 10 s, zeigt der Player die Abrufe beim Server und das Format der Datei.
- **HDR-Filme umwandeln** (HDR10/PQ, HLG): Tonemapping auf SDR (BT.709) mit `zscale`/`tonemap`, sonst
  umgerechnete Farbmatrix. Bisher übernahm FFmpeg die HDR-Kennzeichnung, und der iPhone-Player startete nicht.
- **Spulen beim Umwandeln** schneller: 3-s-Segmente statt 6 s; bei 4K-Quellen kodiert libx264 mit „superfast“.
- **Weboberfläche:** Das Feld „Bestätigungscode (2FA)“ ist auf der Anmeldeseite wieder immer sichtbar. Für die
  Jellyfin-Apps bleibt es bei Konto und Passwort bzw. Quick Connect.
- Jellyfin-Schnittstelle: Nennt eine App „ts“ als Container für HLS, liefert der Server MPEG-TS
  (`SegmentContainer=ts`). `MaxStreamingBitrate` begrenzt auch die Videobitrate der Umwandlung.

## 0.6.6 – 19.11.2026

**Neu: Jellyfin-Apps** (Einstellungen → Jellyfin-Schnittstelle, standardmäßig aus, siehe [docs/jellyfin.md](docs/jellyfin.md))

- Jellyfin-Apps wie Infuse, Swiftfin, Jellyfin für Android TV, Findroid und Streamyfin nutzen dieselbe Mediathek:
  Bibliotheken, Serien/Staffeln/Folgen, Plakate, Suche, „Fortsetzen“, „Als Nächstes“, „Zuletzt hinzugefügt“,
  Favoriten, gesehen/ungesehen. Wiedergabe direkt oder per HLS-Umwandlung, Untertitel. Es gelten dieselben
  Benutzerrechte und Wiedergabepositionen wie in den DS-video-Apps.
- Anmeldung mit DSM-Konto oder Quick Connect (auch für Konten mit Zwei-Faktor-Anmeldung).
- Eigener Port (Standard 8096), zusätzlich über DSM unter `/jellyfin`, automatische Suche im Heimnetz.
- Neuer Reiter „Jellyfin-Apps“ mit Adressen, Quick Connect und angemeldeten Geräten. Jellyfin-Apps erscheinen unter
  „Verbindungen“ und lassen sich trennen.
- **Offizielle App „Jellyfin“ (iPhone/Android):** Sie zeigt nur die Weboberfläche des Servers an, dafür liefert
  DS Video Revival unter `/web/` eine eigene, einfache Mediathek: Startseite mit „Weiterschauen“, „Als Nächstes“ und
  neuen Titeln je Bibliothek, Bibliotheken mit Sortierung, Suche, Detailansicht mit Staffeln, Folgen und Zurück-Knopf,
  Wiedergabe direkt oder umgewandelt mit Untertiteln, Fortsetzen. Anmeldung mit Passwort oder Quick Connect,
  Deutsch/Englisch nach Gerätesprache. Startet ein Film nicht, wird er automatisch stärker umgewandelt.
- Bedingungen aus dem Geräteprofil der Apps (z.B. „H.264 höchstens 8 Bit“) werden beachtet. Die gewählte Tonspur
  entscheidet, ob der Ton kopiert oder umgewandelt wird.

**Weboberfläche**

- Deutsch oder Englisch: Auswahl oben rechts (Automatisch nach Browser/System, Deutsch, English), auch auf der
  Anmeldeseite.
- Der Port der Weboberfläche ist in den Einstellungen änderbar (mit Prüfung auf reservierte und belegte Ports), dazu
  gibt es **„DS Video Revival neu starten“**. Nach einem Portwechsel wechselt die Oberfläche selbst zum neuen Port.
- „Sprache“ der Metadaten heißt jetzt „Sprache der Inhalte (Metadaten)“.

**Wiedergabe (auch in den DS-video-Apps)**

- H.264 mit 10 Bit („High 10“) bzw. 4:2:2/4:4:4 wird nach H.264 8 Bit umgewandelt. Diese Videos können iPhone,
  iPad, Apple TV, Android und Browser nicht abspielen.
- Dolby Vision Profil 7 (UHD-Blu-ray mit Erweiterungsschicht): Die Dolby-Vision-Angabe wird beim Umverpacken
  entfernt, Apple-Geräte spielen dann die HDR10-Basis ab.
- Eine leere `sdtp`-Box, die manche FFmpeg-Versionen schreiben, wird aus umverpackten Videos entfernt. Mit ihr
  spielen Apple-Geräte HEVC-Videos nicht ab.
- Protokoll: Die Wiedergabe-Abrufe der Jellyfin-Schnittstelle stehen für die Fehlersuche im Protokoll
  (`jellyfin-zugriff:`).

## 0.5.42 – 17.11.2026 · erste Veröffentlichung

Erste öffentliche Version auf GitHub. Pakete für x86_64, aarch64 und armv7 (DSM 7.2 oder neuer).

- DS-video-Apps (iOS/iPadOS, Android, Android TV): Filme, Serien, Heimvideos, TV-Aufnahmen, Ansicht „Nach Ordner“,
  „Zuletzt gesehen“, Fortsetzen, Offline-Download und -Umwandlung; Verbindung über die DSM-Adresse ohne Port oder QuickConnect.
- Weboberfläche: Mediathek mit Wiedergabe; Metadaten (Wikidata, Wikipedia, TVmaze, IMDb, optional TMDb) mit Korrektur per IMDb-ID.
- Benutzerrechte: Freigaben, Altersgrenzen, Herunterladen, „Nur eine Verbindung gleichzeitig“ mit Ruhezeit.
- Verbindungen: aktive Geräte anzeigen, trennen und zeitweise sperren.
- Ordneransicht (DSM-Administratoren): Dateipfade, Doppelgänger-Suche mit „Ignorieren“, Löschen, File Station.
- DSM-Updates ohne Neuinstallation des Pakets.
- Anmelden und Abmelden in der Weboberfläche lassen die DSM-Sitzung im selben Browser unberührt.

---

## Entwicklung vor der Veröffentlichung

### 0.5.41
- Weboberfläche: Abmelden meldete auch von DSM ab, weil die Anmeldung das DSM-Sitzungs-Cookie im Browser ersetzte (behoben in 0.5.42).

### 0.5.40
- Ordneransicht, „Nur Doppelgänger“: einzelne Dateien ignorieren (dauerhaft), Liste „Ignorierte Dateien“ mit „Wieder anzeigen“.
- Weboberfläche: Anmelde-Knopf während der Anmeldung gesperrt (keine doppelten Sitzungen).

### 0.5.38 – 16.11.2026
- Apps: „Zuletzt gesehen“ zeigt nur Titel, die das angemeldete Konto angesehen hat, zuletzt gesehene zuerst (Filme, Heimvideos, TV-Aufnahmen, Serien).
- Apps: „Zuletzt hinzugefügt“ bei Serien sortiert nach der neuesten Folge.
- „Nur eine Verbindung gleichzeitig“: Ruhezeit einstellbar (1–240 Minuten, Standard 5).
- Verbindungen: Trennen mit optionaler Gerätesperre (15 Minuten bis 24 Stunden), Liste „Gesperrte Geräte“ mit „Sperre aufheben“.

### 0.5.37 – 16.11.2026
- Serien in der Weboberfläche: vollständige Folgenbeschreibung, Auflösung und Dateigröße je Folge.

### 0.5.36 – 16.11.2026
- Plakate werden auf Gültigkeit geprüft: Fehlende, leere oder kaputte lokale Plakate fallen auf das Plakat aus den Metadaten zurück; liefert TMDb kein Plakat, wird eines aus der Bilderliste genommen.

### 0.5.35 – 16.11.2026
- Manuelle Zuordnung per IMDb-ID mit TMDb-Schlüssel über TMDb (Plakat, Hintergrund, deutsche Texte); ohne Schlüssel Plakat-Rückfall über Wikimedia Commons.
- Zuordnung funktioniert auch für Serien.

### 0.5.34 – 16.11.2026
- Doppelgänger: DVD-Ordner (VIDEO_TS/VOB) verschiedener Discs und Teile wie „1von3“ gelten nicht als Doppelgänger; Folgen ohne erkannte Nummer werden nicht verglichen.
- Detailansicht: IMDb-Feld startet leer; Fenster schließt nicht mehr beim Markieren von Text.

### 0.5.33 – 16.11.2026
- Benutzerrechte: „Nur eine Verbindung gleichzeitig“ für ausgewählte Benutzer/Gruppen.
- Neuer Reiter „Verbindungen“: aktive Geräte (Gerät, Adresse, Zustand, Titel) anzeigen und trennen.

### 0.5.30 – 0.5.32
- Ordneransicht: Filter nach Medienart, Ansicht „Nur Doppelgänger“, Löschen einzelner Dateien und „Alle Doppelgänger löschen“ (beste Datei bleibt); Löschen über DSM, wenn das Paket keine Schreibrechte hat; mehrteilige Filme (CD1/CD2, Teil 1/2 …) sind keine Doppelgänger.
- Detailansicht: Auflösung und Dateigröße unter der Beschreibung; Logo in der Kopfzeile.

### 0.5.24 – 0.5.29
- Ordneransicht mit Dateipfaden (nur DSM-Administratoren), Info-Knopf an den Kacheln, Sprung in die File Station.
- Apps: Ansicht „Nach Ordner“ zeigt alle Medien eines Ordners ohne Durchklicken; Ordnerkacheln mit Plakat-Vorschau.
- DSMs Webserver für lange Wiedergaben eingestellt (Abbrüche nach einigen Minuten behoben).

### 0.5.21 – 0.5.23
- Offline-Download und Offline-Umwandlung in den Apps (hoch/mittel/niedrig).
- Benutzerrechte: wer herunterladen darf.

### 0.5.15 – 0.5.19
- Titel umbenennen und löschen (Administratoren), Bibliotheken und TV-Aufnahmen, eigene Gruppen und Administratoren.

### 0.5.0
- Benutzerrechte: Freigaben je Medienordner und Altersgrenzen (FSK).

### 0.4.x
- Verbindung ohne Portangabe wie früher Video Station: DSMs eigener Webserver reicht die Aufrufe der DS-video-Apps an das Paket weiter; DSM bleibt auf 5000/5001.
- Fortsetzen der Wiedergabe auch bei Apps, die die Position nicht abfragen (ab 0.4.10).

### 0.3.x
- Datenmodelle der Apps vollständig umgesetzt (behebt Absturz beim Antippen).
- Wiedergabe mit Synologys FFmpeg ohne AAC-Encoder (Remux), Analyse ohne ffprobe; Upload- und Update-Probleme im früheren Port-Modus behoben.

### 0.2.0
- Wiedergabe: Direct Play, HLS-Remux, Umwandlung nach H.264 (VAAPI, falls vorhanden); Mediathek im Browser; Metadaten ohne Schlüssel.

### 0.1.x
- Erster Feldtest mit DS video (iPhone): Anmeldung über DSM und Filmliste.
