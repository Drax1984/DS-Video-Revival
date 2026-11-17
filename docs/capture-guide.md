# Mitschnitt-Anleitung: das echte Protokoll aus deinen Clients gewinnen

Ziel: Für jeden Client (iPhone, iPad, Android, Android TV, Apple TV) einmal
aufzeichnen, was die **Original-App** mit einer **Original-Video-Station**
spricht. Danach entfallen die Hypothesen aus `docs/protocol.md`.

Rechtlicher Rahmen: Mitschnitt des eigenen Netzwerkverkehrs zwischen eigener,
rechtmäßig genutzter App und eigener NAS zur Herstellung von Interoperabilität.
Es wird nichts entschlüsselt, keine DRM umgangen, keine App verändert.

## 1. Referenz-Video-Station bereitstellen (eine Option wählen)

- **A – Virtual DSM / Zweit-NAS mit DSM ≤ 7.2.1** und offizieller Video Station
  aus dem Paket-Zentrum (empfohlen, sauber).
- **B – Test-NAS mit 007revad-Skript** (installiert VS 3.1.0-3153 unter DSM 7.2.2+).
  Nur auf einer Testinstallation; vor DSM-Updates muss VS deinstalliert werden.

Ein paar Testdateien einbinden: 2 Filme (1× MP4, 1× MKV), 1 Serie mit 2 Folgen,
1 Heimvideo, je eine externe `.srt`.

## 2. Mitschnitt-Proxy auf deinem Rechner starten

Werkzeug aus `build/tools/` (macOS: `dsvr-macos-arm64`):

```sh
./dsvr-macos-arm64 capture --listen :5081 --upstream http://<IP-der-Referenz-NAS>:5000 --out ds-video-ios.jsonl
```

Passwörter, sids, Tokens und Kontonamen werden **vor dem Schreiben** entfernt.

## 3. App gegen den Proxy verbinden

In der DS-Video-App: Adresse `<IP-deines-Rechners>:5081`, **HTTPS aus**.
Dann nacheinander:

1. Anmelden
2. Jede Bibliothek öffnen (Filme, Serien, Heimvideos)
3. Einen Film öffnen (Details), Poster/Hintergrund laden lassen
4. MP4-Film abspielen, vorspulen, pausieren, beenden
5. Denselben Film erneut öffnen („Fortsetzen“)
6. MKV-Film abspielen
7. Eine Serie → Staffel → Folge abspielen
8. Untertitel und Audiospur wechseln
9. Suche benutzen, einen Favoriten setzen
10. Abmelden

**Fortsetzen-Rückfrage (iPhone) – offene Frage, bitte genau so:**
Proxy **vor** dem Anmelden starten, damit Bibliotheks- und Detailabfragen im
Mitschnitt sind (die Position kann in einer dieser Antworten stecken).
Film ≥ 2 min abspielen → Player schließen → zurück zur Bibliothek → Film erneut
öffnen → Wiedergabe antippen und auf die Rückfrage warten. Einmal „Fortsetzen“,
einmal „Von vorne“ wählen. Den Mitschnitt erst danach beenden.

Pro Gerät eine eigene Datei (`ds-video-tvos.jsonl`, `ds-video-androidtv.jsonl` …).

## 4. Auswerten und zurückgeben

```sh
./dsvr-macos-arm64 analyze ds-video-ios.jsonl --out ds-video-ios.md
```

Die `.md`-Datei (und gern die `.jsonl`) an die Entwicklung geben. Daraus werden
die fehlenden Antwortstrukturen implementiert und als Regressionstests
festgeschrieben.

## 5. Gegenprobe

```sh
DSVR_PASSWORD=… ./dsvr-macos-arm64 probe --base http://<Referenz-NAS>:5000 --account <konto> -v
DSVR_PASSWORD=… ./dsvr-macos-arm64 probe --base http://<NAS-mit-DS-Video-Revival>:5180 --account <konto> -v
```

Beide Ausgaben nebeneinander zeigen Abweichungen in Feldern und Fehlercodes.
