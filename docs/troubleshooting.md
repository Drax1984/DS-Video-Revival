# Fehlersuche

| Symptom | Ursache / Lösung |
|---|---|
| Ordner „nicht lesbar“ in der Verwaltung | Leserecht für Systembenutzer `dsvideorevival` fehlt (Installation, Schritt 2) |
| App: „Verbindung fehlgeschlagen“ | Port in der Adresse vergessen (`NAS-IP:5180`), HTTPS in der App aktiv obwohl nur HTTP läuft, DSM-Firewall |
| App meldet Anmeldefehler, DSM-Login im Browser klappt | Verwaltung → Protokoll: steht dort „DSM nicht erreichbar“, DSM-Adresse in `config.json` (`upstream`) prüfen (z.B. HTTPS-Zwang → `https://127.0.0.1:5001`) |
| Nach mehreren Fehlversuchen keine Anmeldung möglich | Sperre 10 min (je Client-IP). DSM-Autoblock: `127.0.0.1` darf nicht gesperrt sein |
| Film startet nicht, Protokoll: `Unknown encoder 'aac'` / `Unrecognized option 'preset'` | Synologys eigenes FFmpeg (`/bin/ffmpeg`, CodecPack `ffmpeg41`) hat keinen AAC-/libx264-Encoder. Ab 0.3.1: H.264-Filme mit AAC/AC3/E-AC3-Ton laufen auf iPhone/Apple TV trotzdem (reiner Remux). Für DTS-Ton, HEVC/MPEG-4 im Browser: Paket **ffmpeg7** (SynoCommunity) installieren – wird automatisch bevorzugt |
| Film beginnt immer von vorn, obwohl der Fortschrittskreis angezeigt wird | Die iOS-App fragt die gespeicherte Position nicht ab. Ab 0.4.10 setzt der Server beim Öffnen (HLS) die Startstelle selbst auf die zuletzt gemeldete Position (Protokoll: `wiedergabe: Fortsetzen bei …`); Zurückspulen an den Anfang bleibt möglich. Nicht bei < 30 s oder in den letzten 3 Minuten. Spielt die App die Datei direkt (`raw`), bestimmt sie die Startstelle selbst |
| „Transkodierung fehlgeschlagen“, nur „Wiedergeben mit…“ (VLC) geht | Tonspur (DTS, TrueHD, FLAC, PCM …) oder Video (MPEG-2, VC-1, MPEG-4/XviD …) kann das iPhone nicht direkt abspielen, und das Synology-FFmpeg kann es nicht umwandeln (kein AAC-/H.264-Encoder). Lösung: Paket **ffmpeg7** der SynoCommunity installieren (Paket-Zentrum → Einstellungen → Paketquellen: `https://packages.synocommunity.com`), danach DS Video Revival neu starten – wird automatisch verwendet |
| Protokoll: „ffprobe fehlt“ | ab 0.3.1 kein Problem mehr: die Analyse läuft dann über `ffmpeg -i` |
| `TLS handshake error … unknown certificate` | DS video lehnt das selbstsignierte Zertifikat ab. Einstellungen → „HTTPS-Zertifikat“: in DSM exportierte ZIP hochladen, oder in der App HTTPS ausschalten (nur im LAN) |
| Apple TV/iPhone spielt MKV nicht | Verwaltung → Protokoll: Zeile `streaming: … →` zeigt die Entscheidung (remux/transcode) und ggf. den FFmpeg-Fehler |
| Was fragt meine App an? | Verwaltung → Client-Kompatibilität → „Als JSON herunterladen“ und zurückmelden |
| Paket-Zentrum: „Ungültiges Dateiformat“ beim manuellen Installieren, während der Port-Modus aktiv ist | bis 0.3.1 wurden Uploads über Port 5000 bei 8 MB abgeschnitten. Behoben in 0.3.2; zum Einspielen von 0.3.2 DSM direkt über `http://NAS-IP:5010` öffnen |
| Paket-Update bricht ab: „Vorgang fehlgeschlagen … network error“ | DSM wurde über Port 5000 bedient, also durch dieses Paket – beim Update stoppt DSM das Paket und kappt damit die eigene Verbindung. DSM über `http://NAS-IP:5010` öffnen. Ab 0.3.3 leitet das Paket Browser automatisch dorthin weiter |
| Weboberfläche nach Portänderung nicht erreichbar | DSM-Firewall erlaubt den neuen Port nicht, oder der Neustart fehlte. Port in `/var/packages/dsvideorevival/var/config.json` (`listen_http`) prüfen; das Paket im Paket-Zentrum stoppen und starten |
| DSM-Hauptmenü öffnet nach Portänderung den alten Port | DSM im Browser neu laden; sonst das Paket im Paket-Zentrum stoppen und starten |
| Jellyfin-Apps | siehe [jellyfin.md](jellyfin.md#fehlersuche) |
| Protokoll | `/var/packages/dsvideorevival/var/dsvr.log` oder Verwaltung → Protokoll |
