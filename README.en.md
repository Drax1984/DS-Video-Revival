<p align="center"><img src="internal/admin/web/logo-large.webp" width="160" alt="DRAX × BLACK.ZERO – in Cooperation"></p>

# DS Video Revival

**Keep using the original DS video apps after Synology discontinued Video Station.**

Synology removed Video Station with DSM 7.2.2. DS Video Revival is an independent
DSM package (`.spk`) that behaves like the former Video Station towards the
**unmodified original apps** DS video (iOS/iPadOS, Android, Android TV): same
address as DSM, no port needed, sign-in with your DSM account.

🇩🇪 [Deutsche Anleitung](README.md) (full documentation is in German)

> DS Video Revival is not a Synology product and contains no Synology software.
> Synology, DSM, Video Station and DS video are trademarks of Synology Inc. – see [NOTICE.md](NOTICE.md).

## Features

- Movies, TV shows, home videos and TV recordings, multiple libraries – posters, backdrops, plot, cast, age rating, IMDb rating
- Playback: direct, HLS remux or transcoded to H.264/AAC (FFmpeg), audio tracks, subtitles, resume, "Recently watched", folder view
- Offline download and offline conversion in the apps
- Web interface (German): media library in the browser, metadata without API key (Wikidata, Wikipedia, TVmaze, IMDb datasets; TMDb optional),
  user permissions (folders, age limits, downloads, one connection at a time), active connections with disconnect/temporary block,
  folder view with duplicate finder for DSM administrators

## Requirements

- Synology NAS running **DSM 7.2 or later**
- DSM (firmware) updates work as usual – no need to uninstall or reinstall DS Video Revival
- For MKV/AVI, AC3/DTS audio, HEVC etc.: package **ffmpeg7** from [SynoCommunity](https://synocommunity.com)

## Which package for which Synology?

| Package | CPU architecture | CPU examples | Example models |
|---|---|---|---|
| `dsvideorevival-<version>-x86_64.spk` | **x86_64** (Intel/AMD) | Intel Celeron J4125/J4025/J3455, Atom C3538, AMD Ryzen R1600/V1500B | DS224+, DS423+, DS220+, DS420+, DS720+, DS920+, DS918+, DS718+, DS218+, DS923+, DS723+, DS1522+, DS1621+, DS1821+, DS1819+, RS820+ |
| `dsvideorevival-<version>-aarch64.spk` | **aarch64** (ARMv8, 64-bit) | Realtek RTD1296/RTD1619B, Marvell Armada 37xx | DS118, DS218, DS418, DS220j, DS420j, DS223, DS223j, DS423, DS124, DS119j, DS120j, RS819 |
| `dsvideorevival-<version>-armv7.spk` | **armv7** (ARMv7, 32-bit) | Marvell Armada 385/388 | DS116, DS216, DS216j, DS218j, DS416j, DS419slim, RS217, RS816 |

Check DSM → Control Panel → Info Center → *General* → **CPU**: Intel/AMD → x86_64 · Realtek or Marvell Armada 37xx → aarch64 ·
Marvell Armada 38x → armv7. See also Synology's list
["What kind of CPU does my Synology NAS have?"](https://www.synology.com/knowledgebase/DSM/tutorial/General/What_kind_of_CPU_does_my_NAS_have)
(column *Package Arch*: `x86_64` → x86_64, `armv8` → aarch64, `armv7` → armv7). Models without DSM 7.2 are not supported.

## Installation

1. Download the matching package from [**Releases**](../../releases/latest).
2. DSM → Package Center → **Manual Install** → select the `.spk` → confirm the third-party package notice.
3. In the wizard enter your media folders, e.g. `movie:/volume1/video/Movies;tvshow:/volume1/video/TV`.
4. **Grant read access (required):** Control Panel → Shared Folder → *video* → Edit → Permissions →
   drop-down **"System internal user"** → `dsvideorevival` → **Read** (Read/Write if you want to delete from the web interface).
5. DSM main menu → **DS Video Revival** → scan the library.
6. DS video app: same address as for DSM/DS file (IP, domain or QuickConnect ID, **no port**), DSM account – done.

Update: install the newer `.spk` the same way; settings and library are kept.

**DSM updates:** you can update your Synology's firmware (DSM) as usual. There is no need to
uninstall DS Video Revival before or reinstall it afterwards – the package starts again by itself.

## Changelog

See [CHANGELOG.md](CHANGELOG.md) (German).

## License

**Source available, not open source.** Private, non-commercial use of the published packages is permitted;
redistribution, publishing modified versions and commercial use require permission.
Third-party components keep their licenses. See [LICENSE](LICENSE) and [NOTICE.md](NOTICE.md).
