# Kompatibilität

Stand: 0.5.39. ✔ = im Feldtest mit der Original-App bestätigt.

## Clients

| Client | Anmeldung | Bibliothek | Wiedergabe | Weiteres |
|---|---|---|---|---|
| DS video iOS/iPadOS (iPhone) | ✔ | ✔ Filme, Serien, Ordner | ✔ | ✔ Offline-Download/-Umwandlung |
| DS video Android TV | ✔ | ✔ | ✔ | |
| DS video Android (Smartphone) | ✔ | ✔ | ✔ | ✔ Offline-Download/-Umwandlung |
| DS video tvOS (Apple TV) | Rückmeldung offen | | | |
| DS video Fire TV | Rückmeldung offen | | | |
| Weboberfläche (Chrome/Edge/Safari/Firefox, Desktop und Smartphone) | ✔ | ✔ | ✔ | Verwaltung |

Die Original-Weboberfläche von Video Station ist proprietär und wird nicht nachgebaut;
stattdessen gibt es die eigene Weboberfläche (DSM-Hauptmenü → *DS Video Revival*).

## Server

| Bereich | Stand |
|---|---|
| DSM | ab 7.2 (Paket setzt `os_min_ver` 7.2-64561); im Feldtest unter DSM 7.2.x |
| CPU | Pakete für x86_64, aarch64 und armv7 (siehe [README](../README.md#welches-paket-für-welche-synology)) |
| FFmpeg | SynoCommunity-Paket **ffmpeg7** empfohlen; Synologys eigenes FFmpeg nur eingeschränkt (kein AAC-/x264-Encoder) |
| Verbindung ohne Port | über DSMs eigenen Webserver (DSM-Ressource `web-config`, ab DSM 7.2) |
| Fernzugriff | über die DSM-Adresse, QuickConnect oder einen Reverse-Proxy (z.B. Nginx Proxy Manager) |

Probleme und Lösungen: [troubleshooting.md](troubleshooting.md).
