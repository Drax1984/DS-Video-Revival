# Hinweise / Notices

**DS Video Revival ist eine unabhängige Neuentwicklung. Es ist kein Produkt von
Synology Inc., wird von Synology weder unterstützt noch autorisiert und enthält
keine Synology-Software oder -Binärdateien.** Synology, DiskStation, DSM, Video
Station und DS video sind Marken der Synology Inc.

Kompatibilität mit Synology-Protokollen bedeutet keine offizielle Unterstützung.

## Verwendete Fremdinformationen

- `internal/webapi/registry_videostation.json`: API-Namen, Pfade, Versionen und
  Methodennamen der Original-Video-Station, entnommen aus
  [mikespub/synology](https://github.com/mikespub/synology) (`docs/VideoStation*.json`),
  MIT-Lizenz, Copyright (c) 2014 Nicolas Cerveaux. Reine Schnittstellenfakten.
- Protokollverhalten von DS Video (tvOS) bei der Wiedergabe (`Streaming open/stream/close`,
  `hls_remux`/`raw`, Fehler 1211) wurde aus
  [holg/ds-video-hevc](https://github.com/holg/ds-video-hevc) (MIT) als Referenz
  abgeleitet; es wurde kein Code übernommen.
- Request-/Fehlercode-Konventionen: offizieller *Synology File Station API Guide*.
- Jellyfin-Schnittstelle (`internal/jellyfin`): eigene Umsetzung der öffentlich
  beschriebenen Jellyfin-Server-API (OpenAPI), damit Jellyfin-Apps die Mediathek
  nutzen können. Pflichtfelder der Datenmodelle wurden mit den generierten Modellen
  der [jellyfin-sdk-kotlin](https://github.com/jellyfin/jellyfin-sdk-kotlin)
  (LGPL-3.0) abgeglichen; es wurde kein Code übernommen. DS Video Revival enthält
  keine Jellyfin-Software und steht in keiner Verbindung zum Jellyfin-Projekt;
  „Jellyfin“ bezeichnet hier nur die Schnittstelle, die die Apps erwarten.

- Metadaten (Standard, ohne Schlüssel): [Wikidata](https://www.wikidata.org/) (CC0),
  Texte und Plakatbilder aus [Wikipedia](https://www.wikipedia.org/) (Texte CC BY-SA;
  Plakate unterliegen den Rechten der jeweiligen Rechteinhaber),
  Serien aus [TVmaze](https://www.tvmaze.com/) (CC BY-SA).
- IMDb-Bewertungen: *Information courtesy of IMDb (https://www.imdb.com). Used with
  permission.* – aus den nicht-kommerziellen IMDb-Datensätzen (private Nutzung).
- Optional: [TMDB](https://www.themoviedb.org/) über den eigenen API-Schlüssel des Nutzers.
  *This product uses the TMDB API but is not endorsed or certified by TMDB.*
  Optional [OMDb](https://www.omdbapi.com/) (eigener Schlüssel).
- Bilder werden nur lokal zwischengespeichert, nicht weiterverbreitet.

## Enthaltene Fremdsoftware

- `internal/admin/web/hls.light.min.js`: [hls.js](https://github.com/video-dev/hls.js) 1.5.20,
  Apache License 2.0 (`hls.LICENSE.txt`).
- Server: ausschließlich Go-Standardbibliothek (BSD-Lizenz).
- FFmpeg wird **nicht** mitgeliefert, sondern ein vorhandenes genutzt (z.B. SynoCommunity-Paket
  `ffmpeg7`). Grund: Lizenz- und Patentfragen (HEVC, AAC u.a.) liegen damit beim jeweiligen Paket.
