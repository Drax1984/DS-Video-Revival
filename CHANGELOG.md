# Versionsverlauf

Installationspakete: [Releases](../../releases). Welches Paket für welche NAS: [README](README.md#welches-paket-für-welche-synology).

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
