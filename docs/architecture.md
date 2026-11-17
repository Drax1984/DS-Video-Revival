# Architektur

**Entscheidung: Variante B (Neuentwicklung) mit Hybrid-Anteil.**
Variante A (Original-Paket per Kompatibilitätsschicht) scheidet für ein
verteilbares Paket aus: Sie erfordert die Weitergabe proprietärer
Synology-Binärdateien (Abschnitt 18) und eine ältere AME-Version, die andere
Pakete beeinträchtigen kann. Sie bleibt **nur als Referenz für Mitschnitte** relevant.

Hybrid heißt: Alles, was DSM selbst kann (Anmeldung, 2FA, Benutzer, alle
Nicht-Video-Station-APIs), wird an die lokale DSM-WebAPI **durchgereicht**.
Nur die Video-Station-APIs werden selbst implementiert.

```
DS-Video-App ──HTTP(S)──▶ :5180  dsvr (ein statisches Go-Binary, run-as package)
                              │
          ┌───────────────────┼──────────────────────────────┐
          │ SYNO.API.Info     │ SYNO.API.Auth + fremde APIs  │ SYNO.VideoStation(2).*
          │ DSM-Liste + 54    │ unverändert → DSM :5000      │ eigene Handler
          │ VS-Einträge       │ (sid von DSM)                │  ├ Library Engine (Scanner, Index)
          └───────────────────┴──────────────────────────────┤  ├ Streaming Engine (raw/Range; HLS folgt)
                                                             │  ├ Auth Adapter (sid gegen DSM prüfen)
                                                             │  └ Recorder (unbekannte Aufrufe)
                              /dsvr/  Verwaltung (DE, responsiv)
```

## Komponenten (Abschnitt 12)

| | Komponente | Ort | Stand |
|---|---|---|---|
| A | DSM Package Integration | `packaging/synology`, `scripts/package.sh` | SPK je Architektur, DSM-Webserver-Anbindung (`web-config`) |
| B | DS Video Compatibility API | `internal/vs`, `internal/webapi` | Filme, Serien, Heimvideos, TV-Aufnahmen, Ordner, Streaming, Offline |
| C | Authentication Adapter | `internal/auth`, `internal/upstream` | DSM-Durchreichung, lokaler Modus |
| D | Media Library Engine | `internal/library` | Scanner, Parser, Index |
| E | Metadata Engine | `internal/metadata` | Wikidata/Wikipedia/TVmaze/IMDb-Datensätze, TMDb/OMDb optional, lokale Plakate |
| F | Streaming Engine | `internal/stream` | Direct Play, HLS-Remux, Transcode (VAAPI optional), Offline-Umwandlung |
| G | Database Layer | `internal/library` (JSON, atomar) | SQLite geplant (reiner Go-Treiber) |
| H | Web Administration | `internal/admin` | Mediathek, Benutzerrechte, Verbindungen, Ordneransicht, Status, Protokoll |
| I | Diagnostics | `internal/vs/recorder.go`, `capture`, `probe` | Feldforschung eingebaut |
| J | Migration Engine | – | offen |

## Warum eigener Port statt DSM-Port 5000/5001

Ein Drittanbieterpaket kann nach aktuellem Kenntnisstand keine
`SYNO.VideoStation*`-APIs in DSMs eigenes `entry.cgi` eintragen, ohne
DSM-Systemdateien zu verändern (verboten, Abschnitt 8.3). Ein eigener Port ist
nachweislich mit DS Video tvOS nutzbar (Quelle Q5). Folge damals: kein QuickConnect.

Seit 0.4.0 bleibt der eigene Port nur intern: DSMs eigener Webserver reicht die
Aufrufe der DS-video-Apps über die DSM-Ressource `web-config` (ab DSM 7.2) an
das Paket weiter. Die Apps verbinden sich dadurch wie früher mit der normalen
DSM-Adresse auf 5000/5001, ohne dass DSM-Systemdateien verändert werden.
Damit funktioniert auch QuickConnect.

## Ressourcen

Ein Prozess, nur Go-Standardbibliothek, ca. 7–8 MB Binary, Index im Speicher.

Gemessen (x86_64-Container, leere Testdateien, `DSVR_LOAD=1 go test -run TestScanLoad ./internal/library`):

| Dateien | Erstscan | Folgescan | Heap | Filmliste (50 Einträge, sortiert) |
|---|---|---|---|---|
| 10 000 | 0,4 s | 0,1 s | ~10 MB | 10 ms |
| 50 000 | 1,9 s | 0,6 s | ~60 MB | 67 ms |

Auf NAS-CPUs (ARM, Atom) und mit HDDs ist mit deutlich längeren Zeiten zu rechnen
– auf echter Hardware noch nicht gemessen.
