<p align="center"><img src="internal/admin/web/logo-large.webp" width="160" alt="DRAX × BLACK.ZERO – in Cooperation"></p>

# DS Video Revival

**Die Original-DS-video-Apps weiter nutzen – nach dem Aus von Synology Video Station.**

Synology hat Video Station mit DSM 7.2.2 eingestellt. DS Video Revival ist ein
eigenständiges DSM-Paket (`.spk`), das sich gegenüber den **unveränderten
Original-Apps** DS video (iOS/iPadOS, Android, Android TV) wie die frühere
Video Station verhält: gleiche Adresse wie DSM, ohne Portangabe, mit
Anmeldung über das DSM-Konto.

🇬🇧 [English version](README.en.md)

> DS Video Revival ist kein Produkt von Synology Inc. und enthält keine
> Synology-Software. Synology, DSM, Video Station und DS video sind Marken der
> Synology Inc. – siehe [NOTICE.md](NOTICE.md).

---

## Funktionen

**In den DS-video-Apps**
- Filme, Serien (Staffeln/Folgen), Heimvideos und TV-Aufnahmen, mehrere Bibliotheken
- Plakate, Hintergrundbilder, Inhalt, Besetzung, FSK, IMDb-Bewertung
- Wiedergabe: direkt, als HLS-Remux oder umgewandelt nach H.264/AAC (FFmpeg), Tonspuren und Untertitel
- Fortsetzen, „Zuletzt gesehen“, „Zuletzt hinzugefügt“, Ansicht „Nach Ordner“ (mit Plakat-Vorschau)
- Offline-Download und Offline-Umwandlung (Qualitätsstufen hoch/mittel/niedrig)

**Weboberfläche** (DSM-Hauptmenü → *DS Video Revival*)
- Mediathek im Browser mit Wiedergabe, Bewertung, Auflösung und Dateigröße
- Metadaten ohne Schlüssel (Wikidata, Wikipedia, TVmaze, IMDb-Datensätze), optional TMDb; Korrektur per IMDb-ID
- **Benutzerrechte:** Freigaben je Ordner, Altersgrenzen (FSK), Herunterladen, „Nur eine Verbindung gleichzeitig“ mit einstellbarer Ruhezeit
- **Verbindungen:** aktive Geräte sehen, trennen und optional zeitweise sperren
- **Ordneransicht** (DSM-Administratoren): Dateipfade, Doppelgänger-Suche (mit „Ignorieren“), Löschen (Papierkorb), Sprung in die File Station
- Titel umbenennen und löschen, Gruppen, eigene Administratoren
- Oberfläche auf **Deutsch oder Englisch** (automatisch nach Browser/System oder fest gewählt)
- Port der Weboberfläche einstellbar, **Neustart** per Knopf

**Jellyfin-Apps** (optional, ab 0.6.6)
- Eingebaute Jellyfin-Schnittstelle: Infuse, Swiftfin, Jellyfin für Android TV, Findroid oder Streamyfin nutzen dieselbe Mediathek – mit denselben Benutzerrechten und Wiedergabepositionen
- Anmeldung mit DSM-Konto oder per Quick Connect (auch bei Zwei-Faktor-Anmeldung)
- Gedacht als zweites Standbein, falls die DS-video-Apps einmal nicht mehr erhältlich sind – Details: [docs/jellyfin.md](docs/jellyfin.md)

## Voraussetzungen

- Synology NAS mit **DSM 7.2 oder neuer**
- DSM-Updates (Firmware) sind jederzeit wie gewohnt möglich – DS Video Revival muss dafür weder deinstalliert noch neu installiert werden
- Für MKV/AVI, AC3/DTS-Ton, HEVC usw.: Paket **ffmpeg7** aus der [SynoCommunity](https://synocommunity.com)
  (Paket-Zentrum → Einstellungen → Paketquellen → `https://packages.synocommunity.com`)
- Internetzugang der NAS für Plakate und Inhalte (optional)

## Welches Paket für welche Synology?

Es gibt drei Pakete – je nach Prozessor (CPU) der NAS:

| Paket | CPU-Architektur | Prozessor (Beispiele) | Beispielmodelle |
|---|---|---|---|
| `dsvideorevival-<version>-x86_64.spk` | **x86_64** (Intel/AMD) | Intel Celeron J4125/J4025/J3455, Atom C3538, AMD Ryzen R1600/V1500B | DS224+, DS423+, DS220+, DS420+, DS720+, DS920+, DS918+, DS718+, DS218+, DS923+, DS723+, DS1522+, DS1621+, DS1821+, DS1819+, RS820+ |
| `dsvideorevival-<version>-aarch64.spk` | **aarch64** (ARMv8, 64 Bit) | Realtek RTD1296/RTD1619B, Marvell Armada 37xx | DS118, DS218, DS418, DS220j, DS420j, DS223, DS223j, DS423, DS124, DS119j, DS120j, RS819 |
| `dsvideorevival-<version>-armv7.spk` | **armv7** (ARMv7, 32 Bit) | Marvell Armada 385/388 | DS116, DS216, DS216j, DS218j, DS416j, DS419slim, RS217, RS816 |

**So findest du es heraus:** DSM → Systemsteuerung → Info-Center → *Allgemein* → **CPU**.
Intel oder AMD → `x86_64` · Realtek oder Marvell Armada 37xx → `aarch64` · Marvell Armada 38x → `armv7`.
Im Zweifel hilft Synologys Liste [„Welche CPU hat mein Synology NAS?“](https://www.synology.com/knowledgebase/DSM/tutorial/General/What_kind_of_CPU_does_my_NAS_have)
(Spalte *Package Arch*: `x86_64` → x86_64, `armv8` → aarch64, `armv7` → armv7).

> Faustregel: „+“-Modelle haben Intel/AMD (x86_64). Bei „j“- und Einstiegsmodellen unbedingt nachsehen –
> z.B. DS218 ist aarch64, DS218j armv7, DS218+ x86_64.
> Modelle, für die es kein DSM 7.2 gibt, werden nicht unterstützt.

Ein falsches Paket lässt sich zwar installieren, startet aber nicht – dann einfach das richtige darüber installieren.

## Installation

1. Passendes Paket unter [**Releases**](../../releases/latest) herunterladen.
2. DSM → Paket-Zentrum → **Manuelle Installation** → `.spk` wählen → Hinweis „Paket eines Drittanbieters“ bestätigen.
3. Im Assistenten die Medienordner angeben, z.B. `movie:/volume1/video/Filme;tvshow:/volume1/video/Serien`.
4. **Leserechte vergeben (Pflicht):** Systemsteuerung → Freigegebener Ordner → *video* → Bearbeiten →
   Berechtigungen → Dropdown **„Systeminterner Benutzer“** → `dsvideorevival` → **Lesen**
   (für Löschen aus der Weboberfläche: Lesen/Schreiben).
5. DSM-Hauptmenü → **DS Video Revival** → „Bibliothek jetzt einlesen“.
6. DS-video-App: dieselbe Adresse wie für DSM bzw. DS file (IP, Domain oder QuickConnect-ID, **ohne Port**), DSM-Konto, fertig.

Ausführlich – Fernzugriff, Reverse-Proxy (z.B. Nginx Proxy Manager), Firewall, Benutzerrechte:
[**docs/installation.md**](docs/installation.md) · Jellyfin-Apps: [docs/jellyfin.md](docs/jellyfin.md) · Probleme: [docs/troubleshooting.md](docs/troubleshooting.md)

## Update

Neue Version wie oben über **Manuelle Installation** einspielen. Einstellungen, Bibliothek,
Wiedergabepositionen und Benutzerrechte bleiben erhalten. Ein Downgrade ist ebenso möglich.

**DSM-Updates:** Die Firmware der Synology (DSM) lässt sich ganz normal aktualisieren.
DS Video Revival muss dafür weder vorher deinstalliert noch danach neu installiert werden –
das Paket startet nach dem Update wieder von selbst, die Apps verbinden sich wie gewohnt.

## Versionen

| Version | Datum | |
|---|---|---|
| **0.6.7** | 21.11.2026 | App „Jellyfin“: 4K/HEVC auf dem iPhone, Downloads (auch von unterwegs), Android-Anbindung; weniger Last auf der NAS, schnellere Anmeldung |
| 0.6.6 | 19.11.2026 | Jellyfin-Apps (Schnittstelle und Mediathek für die App „Jellyfin“), Oberfläche Deutsch/Englisch, Port einstellbar mit Neustart |
| 0.5.42 | 17.11.2026 | Erste Veröffentlichung |

Änderungen: [**CHANGELOG.md**](CHANGELOG.md)

## Kompatibilität

| Client | Stand |
|---|---|
| DS video iOS/iPadOS | ✔ Anmeldung, Bibliothek, Wiedergabe, Offline-Download |
| DS video Android TV | ✔ Anmeldung, Bibliothek, Wiedergabe |
| DS video Android (Smartphone) | ✔ Anmeldung, Bibliothek, Wiedergabe, Offline-Download |
| DS video Apple TV / Fire TV | Rückmeldungen willkommen |
| Jellyfin-Apps (Infuse, Swiftfin, Jellyfin für Android TV, Findroid, Streamyfin) | neu in 0.6.6 – Rückmeldungen willkommen ([docs/jellyfin.md](docs/jellyfin.md)) |
| App „Jellyfin“ (iPhone) | ✔ Anmeldung lokal und extern, Mediathek, Wiedergabe, Downloads |
| App „Jellyfin“ (Android) | ✔ Anmeldung, Mediathek, Wiedergabe |
| Webbrowser (Weboberfläche) | ✔ Desktop und Smartphone |

Details: [docs/compatibility.md](docs/compatibility.md)

## Datenschutz und Sicherheit

- Anmeldung ausschließlich über DSM (inkl. 2FA/Sperren); das Paket speichert keine Passwörter, Sitzungen nur als SHA-256-Hash.
- Keine Telemetrie. Ins Internet gehen nur Metadaten-Abfragen (Wikidata/Wikipedia/TVmaze/IMDb-Datensätze, optional TMDb/OMDb).
- Jellyfin-Schnittstelle standardmäßig aus; Zugriffstokens nur als SHA-256 gespeichert, je Gerät abmeldbar.
- Mediendateien werden nur gelesen – geändert bzw. gelöscht wird ausschließlich auf ausdrücklichen Befehl eines Administrators.
- Mehr: [docs/security.md](docs/security.md)

## Aus dem Quellcode bauen

Benötigt Go (siehe `go.mod`), keine weiteren Abhängigkeiten.

```sh
scripts/test.sh      # Prüfungen, Unit-/Integrationstests, End-to-End
scripts/package.sh   # → dist/dsvideorevival-<version>-<arch>.spk
```

Architektur und Protokoll: [docs/architecture.md](docs/architecture.md), [docs/protocol.md](docs/protocol.md)

## Lizenz

**Quellcode einsehbar, keine Open-Source-Lizenz.** Private, nicht-kommerzielle Nutzung der
veröffentlichten Pakete ist erlaubt; Weitergabe, Veröffentlichung veränderter Fassungen und
kommerzielle Nutzung nur mit Zustimmung. Bestandteile Dritter behalten ihre Lizenzen.
Siehe [LICENSE](LICENSE) und [NOTICE.md](NOTICE.md).
