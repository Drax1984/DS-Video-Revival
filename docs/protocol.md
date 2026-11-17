# Protokoll-Evidenz

Regel: Nichts wird als „bestätigt“ geführt, was nicht durch eine der unten
genannten Quellen belegt ist. Jede Implementierung trägt einen Status
(`belegt` / `teilbelegt` / `hypothese`), sichtbar in der Verwaltung unter
„Client-Kompatibilität“ und in `internal/vs/handlers.go`.

## Quellen

| ID | Quelle | Art | Was sie belegt |
|---|---|---|---|
| Q1 | Synology *File Station API Guide* (offiziell, PDF) | offiziell | Request-Aufbau (`api`, `version`, `method`, `_sid`, Cookie `id`), Envelope `{"success","data"/"error"}`, Fehlercodes 100–119, `SYNO.API.Info query`, `SYNO.API.Auth login/logout` inkl. Fehler 400–404, `format=sid/cookie`, sid-Ablauf 7 Tage |
| Q2 | `mikespub/synology` `docs/VideoStation*.json` (MIT) | aus Original-`.lib`-Dateien extrahiert | Alle 54 Video-Station-API-Namen, Pfade (`entry.cgi` bzw. `VideoStation/*.cgi`), min/max-Version, Methoden je Version, `requestFormat: JSON` |
| Q3 | `mikespub/synology` `tools/schemas/*.json` | aus realen Antworten abgeleitete Schemas | Antwortfelder von `VideoStation(2).Info`, `Library list`, `Movie list`, `Collection list`, `Folder list`, `Setting.Personal get`, Hüllen von `TVShow/HomeVideo/TVRecording/Episode list`, `API.Auth login` (`did`, `is_portal_port`, `sid`) |
| Q4 | `mikespub/synology` `tools/query.json` | reale DSM-7-Antwort | exakte Form von `SYNO.API.Info`-Einträgen (`requestFormat` nur bei JSON-APIs) |
| Q5 | `holg/ds-video-hevc` (MIT) | funktionierender Proxy, getestet mit DS Video tvOS | Wiedergabekette: `SYNO.API.Auth` v6 login (`session=VideoStation`, `format=sid`) über `entry.cgi`; `SYNO.VideoStation2.Streaming` v1 `open` mit `file={"id":N}` und `hls_remux={"device":"tvos","force_open_vte":false,"audio_track":1,"profile":""}` bzw. `raw={}`; Antwort `{"format","stream_id"}`; Fehler 1211; `GET /webapi/VideoStation/vtestreaming.cgi/<name>?api=SYNO.VideoStation.Streaming&version=1&method=stream&id=…&format=…` mit Range → 206; `close`. **Und:** DS Video akzeptiert einen Drittanbieter-Port (`<nas>:5080`) als Serveradresse. |
| Q6 | Community-Doku (showdoc.com.cn/quickconnect, nur Suchauszüge) | Beispielantworten | Standardbibliotheken (`id 0`, Titel „Movie“, „TVShow“, „Home Video“, „TV Recording“), Stream-ID-Format `<32 hex>_<8 alnum>` |
| Q7 | `zzarbi/synology` `VideoStation/Api.php` | Client-Code | Listenparameter `offset`, `limit`, `sort_by`, `sort_direction`, `additional` (z.B. `["poster_mtime","summary","watched_ratio","collection"]`), Suche mit `title` |
| Q8 | `007revad/Video_Station_for_DSM_722` | Community-Skript | VS 3.1.0-3153 ist die letzte unter DSM 7.2.2+ lauffähige Originalversion |
| Q9 | Original-App DS video 3.4.7 (`com.synology.dsvideo`, APK der veröffentlichten App, mit jadx dekompiliert, nicht weitergegeben) | Client-Code | Exakte Gson-Datenmodelle (`VideoVo`, `VideoV2Vo`, `VideoVo.FileVo`, `AudioTrackVo`, `SubtitleV2Vo`, `PlaybackSettingVo`, `WatchStatusVo`, `OfflineConversion`), Feldtypen (fast alles Text, `rating` int mit −1 = keine, `channel` int, Bitraten long), `extra` als JSON-Text mit `com.synology.TheMovieDb`. **Absturzursache v0.1/v0.2:** `files[0].getDuration().split(":")` und `videoCodec.equals(…)` ohne Null-Prüfung – fehlende Felder lassen die App beim Antippen abstürzen. |

## Belegt durch Q9 (seit 0.3.0 umgesetzt)

- Alle `FileVo`-Felder werden immer gesendet, in den Typen der App
  (`duration` als `HH:MM:SS`, `video_codec`/`audio_codec` nie null).
- `rating`: Ganzzahl, −1 = keine; die App zeigt `rating/10`.
- `actor`/`director`/`writer`/`genre`: Text-Arrays.
- `poster_mtime`/`backdrop_mtime`: Text, nur wenn ein Bild existiert.
- `AudioTrack list` → `trackinfo[{id,codec,channel,language,track}]`;
  `Subtitle list` → Array `{id,title,lang,format,embedded,need_preview}`;
  `File get_playback_setting` → `{audio_track, subtitle_id}`;
  `WatchStatus getinfo` → `watch_status{position (Text), last_update}`.

## Bewusste Hypothesen (offen, durch Feldtest zu klären)

| Punkt | Warum nötig | Wie geklärt wird |
|---|---|---|
| `hls_remux` für nicht direkt abspielbare Dateien: `stream` liefert eine HLS-Playlist (fMP4, absolute `/hls/`-Segmentpfade) statt der Datei | die App reicht die Stream-URL an AVPlayer/ExoPlayer weiter, der am Content-Type HLS erkennt | Feldtest mit MKV auf iPhone/Apple TV |
| Zeitstempel-Format von `poster_mtime` (`YYYY-MM-DD HH:MM:SS.ffffff`) | Typ belegt (Text), Format nicht; die App nutzt es als Cache-Schlüssel | Feldtest; abschaltbar (`send_poster_mtime`) |
| `Subtitle get` liefert SRT-Text (UTF-8) | Inhaltstyp nicht im Modell | Feldtest |
| Port-Modus: Zertifikatsexport über `SYNO.Core.Certificate export` als ZIP | Format nicht offiziell dokumentiert | Feldtest; sonst selbstsigniertes Zertifikat |

## Unbekannt (nicht implementiert)

- Ob/wie DS Video `SYNO.API.Encryption` für den Login nutzt. Im DSM-Modus wird
  alles Auth-bezogene unverändert an DSM durchgereicht, damit ist es abgedeckt,
  falls DSM es unterstützt.
- `SYNO.VideoStation2.Subtitle`, `File get_track_info`, Metadatenliste,
  Sammlungen-CRUD, Renderer (`Controller.*`), Sharing, Kindersicherung, DTV.
- Offline-Konvertierung (ab 0.5.21): Methoden und Antwortmodell aus DS video
  3.4.7 (Android) belegt; Ablauf der iOS-App nicht belegt (Aufrufe werden mit
  `offline:` protokolliert).
- QuickConnect: funktioniert seit der Anbindung über DSMs eigenen Webserver
  (0.4.0), da QuickConnect die Aufrufe an DSM weiterreicht; im Feldtest
  bestätigt. Direkte Weiterleitung von Drittanbieter-Ports per QuickConnect
  gibt es nicht und wird nicht benötigt.

## Metadaten-Quellen (schlüsselfrei, live geprüft 2026-11-09)

| Dienst | Aufruf | Verwendete Felder |
|---|---|---|
| Wikidata | `w/api.php?action=wbsearchentities` → `wbgetentities` | P31 (Film/Serie), P345 IMDb-ID, P4947/P4983 TMDb-ID, P577/P580 Datum, P2047 Laufzeit, P57 Regie, P58 Drehbuch, P161 Darsteller, P136 Genre, P1981 FSK, Sitelinks |
| Wikidata | `action=query&list=search&srsearch=haswbstatement:P345=tt…` | manuelle Zuordnung per IMDb-/TMDb-ID |
| TMDb | `GET /3/find/{tt…}?external_source=imdb_id` → `movie_results`/`tv_results` | manuelle Zuordnung per IMDb-ID mit TMDb-Schlüssel (ab 0.5.35) |
| Commons | `wiki/Special:FilePath/{Datei}?width=500` (Wikidata P3383 Filmplakat) | Plakat-Rückfall ohne TMDb-Schlüssel |
| Wikipedia | `/api/rest_v1/page/summary/<Titel>` | `extract` (Inhalt), `originalimage` (en.wikipedia: Kinoplakat) |
| TVmaze | `/lookup/shows?imdb=…`, `/singlesearch/shows?q=…`, `/shows/<id>/episodebynumber` | Serienplakat, Bewertung, Folgentitel/-bild |
| IMDb | `datasets.imdbws.com/title.ratings.tsv.gz` (wöchentlich) | `averageRating` |
