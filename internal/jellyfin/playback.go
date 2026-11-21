package jellyfin

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/vs"
)

// ---------- Geräteprofil (aus PlaybackInfo) ----------

type codecProfile struct {
	Container  string
	Type       string
	Codec      string // CodecProfiles
	VideoCodec string
	AudioCodec string
	Protocol   string
	Method     string
	Format     string
	Conditions []profileCondition // CodecProfiles
}

type profileCondition struct {
	Condition string // Equals, NotEquals, LessThanEqual, GreaterThanEqual, EqualsAny
	Property  string // VideoBitDepth, VideoProfile, Width, Height, …
	Value     string
}

type deviceProfile struct {
	MaxStreamingBitrate int64
	DirectPlayProfiles  []codecProfile
	TranscodingProfiles []codecProfile
	SubtitleProfiles    []codecProfile
	CodecProfiles       []codecProfile
}

// bitDepth der Videospur (0 = unbekannt).
func bitDepth(p *library.Probe) int {
	if p == nil {
		return 0
	}
	pf, prof := strings.ToLower(p.PixFmt), strings.ToLower(p.VideoProfile)
	switch {
	case strings.Contains(pf, "12"):
		return 12
	case strings.Contains(pf, "10") || strings.Contains(prof, "10"):
		return 10
	case pf != "":
		return 8
	}
	return 0
}

// videoOK: Bedingungen der CodecProfiles (Typ „Video“) für die Videospur
// erfüllt? Unbekannte Eigenschaften und fehlende Angaben gelten als erfüllt.
func (p *deviceProfile) videoOK(pr *library.Probe) bool {
	if p == nil || pr == nil || !pr.OK || pr.VideoCodec == "" {
		return true
	}
	for _, cp := range p.CodecProfiles {
		if !strings.EqualFold(cp.Type, "Video") || strings.TrimSpace(cp.Codec) == "" || !listHas(cp.Codec, pr.VideoCodec) {
			continue
		}
		for _, c := range cp.Conditions {
			if !c.holds(pr) {
				return false
			}
		}
	}
	return true
}

func (c profileCondition) holds(pr *library.Probe) bool {
	num, str := 0, ""
	switch strings.ToLower(c.Property) {
	case "videobitdepth":
		num = bitDepth(pr)
	case "width":
		num = pr.Width
	case "height":
		num = pr.Height
	case "videoprofile":
		if str = strings.ToLower(strings.TrimSpace(pr.VideoProfile)); str == "" {
			return true
		}
	default:
		return true
	}
	if str != "" {
		in := false
		for _, v := range strings.Split(strings.ToLower(c.Value), "|") {
			in = in || strings.TrimSpace(v) == str
		}
		switch strings.ToLower(c.Condition) {
		case "equals", "equalsany":
			return in
		case "notequals":
			return !in
		}
		return true
	}
	v, err := strconv.Atoi(strings.TrimSpace(c.Value))
	if num == 0 || err != nil {
		return true
	}
	switch strings.ToLower(c.Condition) {
	case "equals":
		return num == v
	case "notequals":
		return num != v
	case "lessthanequal":
		return num <= v
	case "greaterthanequal":
		return num >= v
	}
	return true
}

// Namensvarianten von Containern und Codecs zwischen Datei/ffprobe und Profilen.
var aliases = map[string][]string{
	"mp4": {"mp4", "m4v", "mov"}, "mov": {"mov", "mp4"}, "ts": {"ts", "mpegts", "m2ts"},
	"asf": {"asf", "wmv"}, "mpeg": {"mpeg", "mpg", "vob"}, "mkv": {"mkv", "matroska", "webm"},
	"dts": {"dts", "dca"}, "dca": {"dca", "dts"}, "avi": {"avi"},
}

func listHas(list, v string) bool {
	if strings.TrimSpace(list) == "" {
		return true // leer = alles
	}
	if v == "" {
		return true
	}
	cands := aliases[v]
	if cands == nil {
		cands = []string{v}
	}
	for _, p := range strings.Split(strings.ToLower(list), ",") {
		p = strings.TrimSpace(p)
		for _, c := range cands {
			if p == c {
				return true
			}
		}
	}
	return false
}

// progressiveOnly: Die App will Video nur als ganze Datei umgewandelt haben
// (TranscodingProfiles nur mit Protocol „http“) – so fragt z.B. die App
// „Jellyfin“ (iPhone) vor dem Herunterladen. HLS-Adressen sind dafür nutzlos:
// Sie würde die Liste als Videodatei speichern.
func (p *deviceProfile) progressiveOnly() bool {
	if p == nil {
		return false
	}
	video := false
	for _, t := range p.TranscodingProfiles {
		if t.Type != "" && !strings.EqualFold(t.Type, "Video") {
			continue
		}
		video = true
		if t.Protocol == "" || strings.EqualFold(t.Protocol, "hls") {
			return false
		}
	}
	return video
}

func (p *deviceProfile) canDirectPlay(cont, vcodec, acodec string) bool {
	if p == nil {
		return true
	}
	for _, d := range p.DirectPlayProfiles {
		if d.Type != "" && !strings.EqualFold(d.Type, "Video") {
			continue
		}
		if listHas(d.Container, cont) && listHas(d.VideoCodec, vcodec) && listHas(d.AudioCodec, acodec) {
			return true
		}
	}
	return false
}

// subtitleMethod: wie das Gerät einen Untertitel möchte (Embed/External).
func (p *deviceProfile) subtitleMethod(codec string, text bool) string {
	if p == nil || len(p.SubtitleProfiles) == 0 {
		return "Embed"
	}
	f := subFormat(codec)
	for _, s := range p.SubtitleProfiles {
		if strings.EqualFold(s.Method, "Embed") && (strings.EqualFold(s.Format, codec) || strings.EqualFold(s.Format, f)) {
			return "Embed"
		}
	}
	if text {
		for _, s := range p.SubtitleProfiles {
			if strings.EqualFold(s.Method, "External") && (strings.EqualFold(s.Format, "srt") || strings.EqualFold(s.Format, "vtt") || strings.EqualFold(s.Format, "subrip")) {
				return "External"
			}
		}
	}
	return "Embed"
}

// externalFormat: Format für externe Untertitel (vtt, falls gewünscht, sonst srt).
func (p *deviceProfile) externalFormat() string {
	if p != nil {
		for _, s := range p.SubtitleProfiles {
			if strings.EqualFold(s.Method, "External") && strings.EqualFold(s.Format, "vtt") {
				return "vtt"
			}
		}
		for _, s := range p.SubtitleProfiles {
			if strings.EqualFold(s.Method, "External") && (strings.EqualFold(s.Format, "srt") || strings.EqualFold(s.Format, "subrip")) {
				return "srt"
			}
		}
	}
	return "srt"
}

func subFormat(codec string) string {
	switch strings.ToLower(codec) {
	case "subrip", "srt":
		return "srt"
	case "ass", "ssa":
		return "ass"
	case "webvtt", "vtt":
		return "vtt"
	case "hdmv_pgs_subtitle", "pgssub":
		return "pgssub"
	case "dvd_subtitle", "dvdsub":
		return "dvdsub"
	case "mov_text":
		return "mov_text"
	}
	return strings.ToLower(codec)
}

var textSubCodecs = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "vtt": true, "text": true}

// ---------- Spuren ----------

// track: Spur mit Jellyfin-Index und Bezug zur Datei.
type track struct {
	stream  map[string]any
	audio   int    // relative Tonspur (−1 = keine)
	subID   string // Untertitel-ID für vs.Server.SubtitleText ("" = Bilduntertitel)
	isText  bool
	codec   string
	isSub   bool
	isAudio bool
}

func vsSubtitles(f library.File) []vs.SubtitleTrack { return vs.Subtitles(f) }

func langLabel(lang string) string {
	names := map[string]string{"ger": "Deutsch", "deu": "Deutsch", "de": "Deutsch", "eng": "English", "en": "English",
		"fre": "Français", "fra": "Français", "fr": "Français", "spa": "Español", "es": "Español", "ita": "Italiano", "it": "Italiano",
		"jpn": "日本語", "ja": "日本語", "tur": "Türkçe", "tr": "Türkçe", "pol": "Polski", "rus": "Русский", "dut": "Nederlands", "nld": "Nederlands"}
	if n := names[strings.ToLower(lang)]; n != "" {
		return n
	}
	return lang
}

func channelLayout(ch int) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return ""
}

func codecLabel(c string) string {
	m := map[string]string{"h264": "H264", "hevc": "HEVC", "aac": "AAC", "ac3": "Dolby Digital", "eac3": "Dolby Digital+",
		"dts": "DTS", "truehd": "TrueHD", "mp3": "MP3", "flac": "FLAC", "opus": "Opus", "subrip": "SRT", "ass": "ASS",
		"hdmv_pgs_subtitle": "PGS", "dvd_subtitle": "VOBSUB", "mpeg2video": "MPEG-2", "mpeg4": "MPEG-4", "vc1": "VC-1", "av1": "AV1", "vp9": "VP9"}
	if v := m[strings.ToLower(c)]; v != "" {
		return v
	}
	return strings.ToUpper(c)
}

func resLabel(w, hgt int) string {
	switch {
	case w >= 3800 || hgt >= 2100:
		return "4K"
	case w >= 1900 || hgt >= 1060:
		return "1080p"
	case w >= 1260 || hgt >= 700:
		return "720p"
	case hgt > 0:
		return strconv.Itoa(hgt) + "p"
	}
	return ""
}

// tracks baut die MediaStreams einer Datei. transcode: Wiedergabe über HLS
// (Untertitel dann extern).
func (h *Handler) tracks(x *req, it library.Item, f library.File, prof *deviceProfile, transcode bool) []track {
	var out []track
	base := map[string]any{"IsInterlaced": false, "IsDefault": false, "IsForced": false, "IsHearingImpaired": false,
		"IsOriginal": false, "IsExternal": false, "IsTextSubtitleStream": false, "SupportsExternalStream": false}
	mk := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		m["Index"] = len(out)
		return m
	}
	p := f.Probe
	if p != nil && p.OK && (p.VideoCodec != "" || p.Width > 0) {
		v := map[string]any{"Type": "Video", "Codec": p.VideoCodec, "IsDefault": true, "Width": p.Width, "Height": p.Height,
			"DisplayTitle": strings.TrimSpace(resLabel(p.Width, p.Height) + " " + codecLabel(p.VideoCodec))}
		if p.FrameRate > 0 {
			v["AverageFrameRate"], v["RealFrameRate"] = p.FrameRate, p.FrameRate
		}
		if br := p.VideoBitrate; br > 0 {
			v["BitRate"] = br
		} else if p.Bitrate > 0 {
			v["BitRate"] = p.Bitrate
		}
		if p.VideoProfile != "" {
			v["Profile"] = p.VideoProfile
		}
		if p.PixFmt != "" {
			v["PixelFormat"] = p.PixFmt
			if !strings.Contains(p.PixFmt, "10") && !strings.Contains(p.PixFmt, "12") {
				v["VideoRange"], v["VideoRangeType"] = "SDR", "SDR"
				v["BitDepth"] = 8
			} else {
				v["BitDepth"] = 10
			}
		}
		if p.Width > 0 && p.Height > 0 {
			v["AspectRatio"] = aspect(p.Width, p.Height)
		}
		out = append(out, track{stream: mk(v), audio: -1, codec: p.VideoCodec})
	}
	if p != nil && p.OK {
		auds := p.AudioTracks
		if len(auds) == 0 && p.AudioCodec != "" {
			auds = []library.StreamTrack{{Index: 0, Codec: p.AudioCodec, Channels: p.Channels}}
		}
		for i, a := range auds {
			title := strings.TrimSpace(strings.Join(nonEmpty(langLabel(a.Language), codecLabel(a.Codec), channelLayout(a.Channels)), " "))
			if a.Title != "" {
				title = a.Title + " – " + title
			}
			s := map[string]any{"Type": "Audio", "Codec": a.Codec, "Language": a.Language, "DisplayTitle": title, "IsDefault": i == 0}
			if a.Title != "" {
				s["Title"] = a.Title
			}
			if a.Channels > 0 {
				s["Channels"] = a.Channels
				s["ChannelLayout"] = channelLayout(a.Channels)
			}
			if i == 0 && p.AudioBitrate > 0 {
				s["BitRate"] = p.AudioBitrate
			}
			if a.Profile != "" {
				s["Profile"] = a.Profile
			}
			out = append(out, track{stream: mk(s), audio: i, codec: a.Codec, isAudio: true})
		}
	}
	extFmt := prof.externalFormat()
	addSub := func(s map[string]any, subID, codec string, text, external bool) {
		t := track{audio: -1, subID: subID, isText: text, codec: codec, isSub: true}
		s["Type"], s["Codec"] = "Subtitle", codec
		s["IsTextSubtitleStream"], s["SupportsExternalStream"] = text, text
		s["IsExternal"] = external
		m := mk(s)
		method := "Embed"
		switch {
		case external, transcode && text:
			method = "External"
		case transcode:
			method = "Drop" // Bilduntertitel werden beim Umwandeln nicht eingebrannt
		default:
			method = prof.subtitleMethod(codec, text)
		}
		m["DeliveryMethod"] = method
		if method == "External" {
			m["DeliveryUrl"] = fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/0/Stream.%s?api_key=%s",
				makeID(kItem, uint64(it.ID)), makeID(kFile, uint64(f.ID)), m["Index"], extFmt, url.QueryEscape(x.auth.Token))
			m["IsExternalUrl"] = false
		}
		t.stream = m
		out = append(out, t)
	}
	subs := vsSubtitles(f)
	if p != nil && p.OK {
		for i, s := range p.Subtitles {
			text := textSubCodecs[strings.ToLower(s.Codec)]
			id := ""
			if text {
				id = "emb:" + strconv.Itoa(s.Index)
				_ = i
			}
			title := strings.TrimSpace(strings.Join(nonEmpty(langLabel(s.Language), codecLabel(s.Codec)), " "))
			if s.Title != "" {
				title = s.Title + " – " + title
			}
			addSub(map[string]any{"Language": s.Language, "DisplayTitle": title, "Title": s.Title,
				"IsForced": strings.Contains(strings.ToLower(s.Title), "forced")}, id, s.Codec, text, false)
		}
	}
	for _, s := range subs {
		if s.Embedded {
			continue
		}
		codec := map[string]string{"srt": "subrip", "vtt": "webvtt", "ass": "ass"}[s.Format]
		if codec == "" {
			codec = s.Format
		}
		addSub(map[string]any{"Language": s.Lang, "DisplayTitle": s.Title + " (extern)", "Title": s.Title,
			"IsForced": strings.Contains(strings.ToLower(s.Title), "forced")}, s.ID, codec, true, true)
	}
	return out
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func aspect(w, hgt int) string {
	g := func(a, b int) int {
		for b != 0 {
			a, b = b, a%b
		}
		return a
	}(w, hgt)
	r := float64(w) / float64(hgt)
	switch {
	case math.Abs(r-16.0/9) < 0.02:
		return "16:9"
	case math.Abs(r-4.0/3) < 0.02:
		return "4:3"
	case math.Abs(r-2.39) < 0.05:
		return "2.39:1"
	}
	return fmt.Sprintf("%d:%d", w/g, hgt/g)
}

// ---------- MediaSources ----------

// pbOpts: Wunsch der App für diese Wiedergabe (PlaybackInfo).
type pbOpts struct {
	Profile              *deviceProfile
	MaxStreamingBitrate  int64
	StartTimeTicks       int64
	AudioStreamIndex     *int
	SubtitleStreamIndex  *int
	MediaSourceID        string
	EnableDirectPlay     *bool
	EnableDirectStream   *bool
	EnableTranscoding    *bool
	AllowVideoStreamCopy *bool
	AllowAudioStreamCopy *bool
	PlaySessionID        string
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func fileBitrate(f library.File) int64 {
	if f.Probe != nil && f.Probe.Bitrate > 0 {
		return f.Probe.Bitrate
	}
	if d := fileDuration(f, library.Item{}); d > 0 && f.Size > 0 {
		return int64(float64(f.Size*8) / d)
	}
	return 0
}

func sourceName(f library.File, n int) string {
	if n <= 1 {
		return strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	}
	label := ""
	if f.Probe != nil && f.Probe.OK {
		label = strings.TrimSpace(resLabel(f.Probe.Width, f.Probe.Height) + " " + codecLabel(f.Probe.VideoCodec))
	}
	if label == "" {
		return filepath.Base(f.Path)
	}
	return label + " – " + filepath.Base(f.Path)
}

// mediaSources: je Datei (Version) eine MediaSource. opts == nil: nur Angaben
// (Listen); sonst Entscheidung Direktwiedergabe/Umwandlung für diese App.
func (h *Handler) mediaSources(x *req, it library.Item, files []library.File, opts *pbOpts) []map[string]any {
	out := []map[string]any{}
	var prof *deviceProfile
	if opts != nil {
		prof = opts.Profile
	}
	canTranscode := h.S.HLS != nil && h.S.HLS.FFmpeg != ""
	progressive := opts != nil && prof.progressiveOnly()
	for _, f := range files {
		cont := container(f.Path)
		vcodec, acodec := "", ""
		if f.Probe != nil && f.Probe.OK {
			vcodec, acodec = f.Probe.VideoCodec, f.Probe.AudioCodec
		}
		direct := true
		reasons := []string{}
		if opts != nil {
			trs := h.tracks(x, it, f, prof, false)
			if opts.AudioStreamIndex != nil {
				for _, t := range trs {
					if t.isAudio && t.stream["Index"] == *opts.AudioStreamIndex {
						acodec = t.codec
					}
				}
			}
			if !boolOr(opts.EnableDirectPlay, true) {
				direct = false
				reasons = append(reasons, "DirectPlayError")
			} else if !prof.canDirectPlay(cont, vcodec, acodec) {
				direct = false
				reasons = append(reasons, "ContainerNotSupported")
			} else if !prof.videoOK(f.Probe) {
				direct = false
				reasons = append(reasons, "VideoBitDepthNotSupported")
			} else if opts.MaxStreamingBitrate > 0 && fileBitrate(f) > opts.MaxStreamingBitrate {
				direct = false
				reasons = append(reasons, "ContainerBitrateExceedsLimit")
			}
			if !direct && !progressive && (!canTranscode || !boolOr(opts.EnableTranscoding, true)) {
				direct = true // lieber direkt versuchen als gar nicht
			}
		}
		trs := h.tracks(x, it, f, prof, !direct)
		streams := make([]map[string]any, len(trs))
		defAudio := -1
		for i, t := range trs {
			streams[i] = t.stream
			if t.isAudio && defAudio < 0 {
				defAudio = i
			}
		}
		msid := makeID(kFile, uint64(f.ID))
		src := map[string]any{
			"Protocol": "File", "Id": msid, "Type": "Default", "Container": cont, "Size": f.Size, "Name": sourceName(f, len(files)),
			"IsRemote": false, "ETag": strconv.FormatInt(f.MTime, 16), "ReadAtNativeFramerate": false, "IgnoreDts": false, "IgnoreIndex": false,
			"GenPtsInput": false, "SupportsTranscoding": canTranscode, "SupportsDirectStream": direct, "SupportsDirectPlay": direct,
			"IsInfiniteStream": false, "UseMostCompatibleTranscodingProfile": false, "RequiresOpening": false, "RequiresClosing": false,
			"RequiresLooping": false, "SupportsProbing": true, "VideoType": "VideoFile", "MediaStreams": streams,
			"MediaAttachments": []any{}, "Formats": []string{}, "RequiredHttpHeaders": map[string]string{},
			"TranscodingSubProtocol": "http", "HasSegments": false,
		}
		if dur := fileDuration(f, it); dur > 0 {
			src["RunTimeTicks"] = ticks(dur)
		}
		if br := fileBitrate(f); br > 0 {
			src["Bitrate"] = br
		}
		if defAudio >= 0 {
			src["DefaultAudioStreamIndex"] = defAudio
		}
		if progressive && !direct {
			// Herunterladen in der App: fertige Umwandlung der Mediathek (MP4)
			// als eigene Quelle anbieten; sonst lädt die App die Adresse, die ihr
			// die Mediathek mitgegeben hat.
			src["SupportsTranscoding"] = false
			if off, ok := h.offlineSource(x, it, f); ok {
				out = append(out, off)
			}
		} else if opts != nil && !direct {
			o := *opts
			if !prof.videoOK(f.Probe) { // Videospur passt nicht zum Gerät: nicht nur umverpacken
				no := false
				o.AllowVideoStreamCopy = &no
			}
			tu := h.transcodingURL(x, it, f, &o, reasons)
			src["TranscodingUrl"] = tu
			src["TranscodingSubProtocol"] = "hls"
			src["TranscodingContainer"] = "mp4"
			if strings.Contains(tu, "SegmentContainer=ts") {
				src["TranscodingContainer"] = "ts"
			}
		}
		out = append(out, src)
	}
	if progressive {
		for _, src := range out {
			h.downloadViaBase(x, it, src)
		}
	}
	return out
}

// downloadViaBase: Herunterladen in der App „Jellyfin“ (iPhone) über einen
// Basis-Pfad, z.B. von unterwegs über DSM (https://nas.example.com/jellyfin).
// Für Direktwiedergabe baut die App die Adresse selbst ab der Wurzel des
// Servers („/Videos/{id}/stream.mp4“, new URL(pfad, server)) – der Pfad
// /jellyfin fällt dabei weg, der Abruf landet bei DSM statt hier und die App
// speichert dessen Fehlerseite (wenige KB) als Video. Die Adresse einer
// Umwandlung übernimmt sie dagegen wie geliefert: Die Datei wird daher als
// „Umwandlung“ (ganze Datei per HTTP) mit vollständigem Pfad angeboten.
func (h *Handler) downloadViaBase(x *req, it library.Item, src map[string]any) {
	base := vs.JellyfinBase(x.r)
	id, _ := src["Id"].(string)
	if base == "" || id == "" || src["SupportsDirectPlay"] != true {
		return
	}
	v := url.Values{}
	v.Set("MediaSourceId", id)
	v.Set("api_key", x.auth.Token)
	src["SupportsDirectPlay"], src["SupportsDirectStream"], src["SupportsTranscoding"] = false, false, true
	src["TranscodingUrl"] = base + "/Items/" + makeID(kItem, uint64(it.ID)) + "/Download?" + v.Encode()
	src["TranscodingSubProtocol"] = "http"
	src["TranscodingContainer"] = src["Container"]
}

// dlSessionPrefix kennzeichnet PlaySessionIds von Abfragen zum Herunterladen
// (Download-Profil der App). Deren „Wiedergabe beendet“ nach dem Laden ist
// keine Wiedergabe und ändert die Position nicht.
const dlSessionPrefix = "dsvrdl"

// offlineSourcePrefix: MediaSourceId einer fertigen Umwandlung (Aufgaben-Nr.).
const offlineSourcePrefix = "dsvroff"

// offlineSource: fertige Umwandlung „apple“ der Datei als MediaSource
// (Direktwiedergabe, MP4).
func (h *Handler) offlineSource(x *req, it library.Item, f library.File) (map[string]any, bool) {
	if h.S.Offline == nil {
		return nil, false
	}
	t, ok := h.offlineTask(x.sess.Account, f.ID, "apple")
	if !ok || t.State != stream.OfflineDone {
		return nil, false
	}
	t, _, ok = h.doneOffline(x, t.ID)
	if !ok {
		return nil, false
	}
	streams := []map[string]any{}
	if pr := t.OutProbe; pr != nil && pr.OK {
		streams = append(streams, map[string]any{"Type": "Video", "Index": 0, "Codec": pr.VideoCodec, "Width": pr.Width, "Height": pr.Height,
			"IsDefault": true, "IsExternal": false, "IsTextSubtitleStream": false, "SupportsExternalStream": false, "Profile": pr.VideoProfile})
		if pr.AudioCodec != "" {
			streams = append(streams, map[string]any{"Type": "Audio", "Index": 1, "Codec": pr.AudioCodec, "Channels": pr.Channels,
				"IsDefault": true, "IsExternal": false, "IsTextSubtitleStream": false, "SupportsExternalStream": false})
		}
	}
	name := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)) + ".mp4"
	src := map[string]any{
		"Protocol": "File", "Id": offlineSourcePrefix + strconv.Itoa(t.ID), "Type": "Default", "Container": "mp4", "Size": t.OutSize,
		"Name": "iPhone (MP4)", "Path": name, "IsRemote": false, "ETag": strconv.FormatInt(t.Finished, 16),
		"SupportsTranscoding": false, "SupportsDirectStream": true, "SupportsDirectPlay": true, "IsInfiniteStream": false,
		"RequiresOpening": false, "RequiresClosing": false, "RequiresLooping": false, "SupportsProbing": true, "VideoType": "VideoFile",
		"MediaStreams": streams, "MediaAttachments": []any{}, "Formats": []string{}, "RequiredHttpHeaders": map[string]string{},
		"HasSegments": false,
	}
	if d := t.Src.Duration; d > 0 {
		src["RunTimeTicks"] = ticks(d)
		src["Bitrate"] = int64(float64(t.OutSize*8) / d)
	}
	if len(streams) > 1 {
		src["DefaultAudioStreamIndex"] = 1
	}
	return src, true
}

func (h *Handler) transcodingURL(x *req, it library.Item, f library.File, o *pbOpts, reasons []string) string {
	v := url.Values{}
	v.Set("MediaSourceId", makeID(kFile, uint64(f.ID)))
	v.Set("PlaySessionId", o.PlaySessionID)
	v.Set("api_key", x.auth.Token)
	if x.auth.DeviceID != "" {
		v.Set("DeviceId", x.auth.DeviceID)
	}
	vc, ac := "h264", "aac"
	if o.Profile != nil {
		for _, t := range o.Profile.TranscodingProfiles {
			if t.Type == "" || strings.EqualFold(t.Type, "Video") {
				if t.VideoCodec != "" {
					vc = t.VideoCodec
				}
				if t.AudioCodec != "" {
					ac = t.AudioCodec
				}
				if strings.EqualFold(t.Container, "ts") && (t.Protocol == "" || strings.EqualFold(t.Protocol, "hls")) {
					v.Set("SegmentContainer", "ts") // wie Jellyfin: MPEG-TS-Segmente
				}
				break
			}
		}
	}
	v.Set("VideoCodec", vc)
	v.Set("AudioCodec", ac)
	if o.AudioStreamIndex != nil {
		v.Set("AudioStreamIndex", strconv.Itoa(*o.AudioStreamIndex))
	}
	if o.StartTimeTicks > 0 {
		v.Set("StartTimeTicks", strconv.FormatInt(o.StartTimeTicks, 10))
	}
	if o.MaxStreamingBitrate > 0 {
		v.Set("MaxStreamingBitrate", strconv.FormatInt(o.MaxStreamingBitrate, 10))
	}
	if o.AllowVideoStreamCopy != nil {
		v.Set("AllowVideoStreamCopy", strconv.FormatBool(*o.AllowVideoStreamCopy))
	}
	if o.AllowAudioStreamCopy != nil {
		v.Set("AllowAudioStreamCopy", strconv.FormatBool(*o.AllowAudioStreamCopy))
	}
	v.Set("TranscodeReasons", strings.Join(reasons, ","))
	return "/videos/" + makeID(kItem, uint64(it.ID)) + "/master.m3u8?" + v.Encode()
}

// playbackInfo: GET/POST /Items/{id}/PlaybackInfo.
func (h *Handler) playbackInfo(x *req, id string) {
	e, ok := h.resolve(x, id)
	if !ok || e.kind != kItem {
		http.NotFound(x.w, x.r)
		return
	}
	var body struct {
		DeviceProfile        *deviceProfile
		MaxStreamingBitrate  int64
		StartTimeTicks       int64
		AudioStreamIndex     *int
		SubtitleStreamIndex  *int
		MediaSourceId        string
		EnableDirectPlay     *bool
		EnableDirectStream   *bool
		EnableTranscoding    *bool
		AllowVideoStreamCopy *bool
		AllowAudioStreamCopy *bool
	}
	readBody(x.r, &body)
	o := &pbOpts{Profile: body.DeviceProfile, MaxStreamingBitrate: body.MaxStreamingBitrate, StartTimeTicks: body.StartTimeTicks,
		AudioStreamIndex: body.AudioStreamIndex, SubtitleStreamIndex: body.SubtitleStreamIndex, MediaSourceID: body.MediaSourceId,
		EnableDirectPlay: body.EnableDirectPlay, EnableDirectStream: body.EnableDirectStream, EnableTranscoding: body.EnableTranscoding,
		AllowVideoStreamCopy: body.AllowVideoStreamCopy, AllowAudioStreamCopy: body.AllowAudioStreamCopy}
	// Query-Parameter haben Vorrang (so senden es die meisten Apps).
	if v := x.QInt64("MaxStreamingBitrate", 0); v > 0 {
		o.MaxStreamingBitrate = v
	}
	if v := x.QInt64("StartTimeTicks", -1); v >= 0 {
		o.StartTimeTicks = v
	}
	if v := x.QInt("AudioStreamIndex", -1); v >= 0 {
		o.AudioStreamIndex = &v
	}
	if v := x.QInt("SubtitleStreamIndex", -2); v >= -1 {
		o.SubtitleStreamIndex = &v
	}
	if v := x.Q("MediaSourceId"); v != "" {
		o.MediaSourceID = v
	}
	for name, dst := range map[string]**bool{"EnableDirectPlay": &o.EnableDirectPlay, "EnableDirectStream": &o.EnableDirectStream,
		"EnableTranscoding": &o.EnableTranscoding, "AllowVideoStreamCopy": &o.AllowVideoStreamCopy, "AllowAudioStreamCopy": &o.AllowAudioStreamCopy} {
		if v, ok := x.QBool(name); ok {
			b := v
			*dst = &b
		}
	}
	if o.Profile != nil && o.MaxStreamingBitrate == 0 {
		o.MaxStreamingBitrate = o.Profile.MaxStreamingBitrate
	}
	o.PlaySessionID = randHex(16)
	if o.Profile.progressiveOnly() {
		o.PlaySessionID = dlSessionPrefix + randHex(13)
	}
	files := h.S.Lib.Files(e.it)
	if o.MediaSourceID != "" {
		if k, p, ok := parseID(o.MediaSourceID); ok && k == kFile {
			for _, f := range files {
				if f.ID == int(p) {
					files = []library.File{f}
					break
				}
			}
		}
	}
	for i, f := range files { // technische Daten nachladen (wie die Weboberfläche)
		if h.S.FFprobe != "" && (f.Probe.NeedsProbe(h.S.FFprobe) || f.Probe.NeedsProfiles(h.S.FFprobe)) {
			files[i].Probe = library.RunProbe(h.S.FFprobe, f.Path)
			h.S.Lib.SetProbe(f.ID, files[i].Probe)
		}
	}
	srcs := h.mediaSources(x, e.it, files, o)
	if len(srcs) == 0 {
		writeJSON(x.w, map[string]any{"MediaSources": []any{}, "PlaySessionId": o.PlaySessionID, "ErrorCode": "NoCompatibleStream"})
		return
	}
	writeJSON(x.w, map[string]any{"MediaSources": srcs, "PlaySessionId": o.PlaySessionID})
}

// ---------- Direktwiedergabe und Download ----------

// fileFor: Datei zu MediaSourceId (oder erste Version des Objekts).
func (h *Handler) fileFor(x *req, itemID, msid string) (library.File, library.Item, bool) {
	if k, p, ok := parseID(msid); ok && k == kFile {
		if f, ok := x.view.File(int(p)); ok {
			it, ok := x.view.Item(f.ItemID)
			return f, it, ok
		}
	}
	e, ok := h.resolve(x, itemID)
	if !ok || e.kind != kItem {
		return library.File{}, library.Item{}, false
	}
	files := h.S.Lib.Files(e.it)
	if len(files) == 0 {
		return library.File{}, library.Item{}, false
	}
	if st := h.state(x, e.it); st.FileID != 0 {
		for _, f := range files {
			if f.ID == st.FileID {
				return f, e.it, true
			}
		}
	}
	return files[0], e.it, true
}

func (h *Handler) title(it library.Item) string {
	if it.Kind == "episode" {
		return fmt.Sprintf("%s S%02dE%02d", it.Show, it.Season, it.Episode)
	}
	return it.DisplayTitle()
}

func (h *Handler) streamFile(x *req, itemID string, download bool) {
	if x.r.Method != http.MethodGet && x.r.Method != http.MethodHead {
		jsonError(x.w, http.StatusMethodNotAllowed, "GET")
		return
	}
	if ms := strings.ToLower(x.Q("MediaSourceId")); strings.HasPrefix(ms, offlineSourcePrefix) {
		if id, err := strconv.Atoi(strings.TrimPrefix(ms, offlineSourcePrefix)); err == nil {
			h.serveOffline(x, id)
			return
		}
	}
	f, it, ok := h.fileFor(x, itemID, x.Q("MediaSourceId"))
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	if h.S.ConnBlocked(x.sess) {
		jsonError(x.w, http.StatusForbidden, "Konto ist auf einem anderen Gerät aktiv")
		return
	}
	if download {
		if !h.S.MayDownload(x.sess) {
			jsonError(x.w, http.StatusForbidden, "Herunterladen nicht erlaubt")
			return
		}
		x.w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(f.Path)}))
	}
	key := x.sess.Key
	tok := "jfraw:" + strconv.Itoa(f.ID)
	h.S.Conns().Play(key, tok, h.title(it))
	gone := h.S.Conns().Begin(key, true)
	defer h.S.Conns().End(key, true)
	w, done := h.track(x, makeID(kItem, uint64(it.ID)), f.Size)
	defer done()
	stream.ServeRaw(vs.GuardWriter(w, gone), x.r, f.Path)
}

// ---------- HLS (Umwandlung) ----------

type playSession struct {
	Job     string
	Key     string
	Created time.Time
	Sig     string // Abfrage, die den Job gestartet hat
	Master  string // ausgelieferte master.m3u8
}

// serveText liefert eine Liste mit Range-Unterstützung (Safari/WebKit fragt
// Adressen zuerst mit „Range: bytes=0-1“ ab).
func serveText(w http.ResponseWriter, r *http.Request, name, body string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(body))
}

// master: /videos/{id}/master.m3u8 – Übersichtsliste mit einer Variante.
func (h *Handler) master(x *req, itemID string) {
	if _, body, ok := h.hlsStart(x, itemID); ok {
		serveText(x.w, x.r, "master.m3u8", body)
	}
}

// mainPlaylist: /videos/{id}/main.m3u8 – direkt die Liste der Segmente, ohne
// master.m3u8 davor (so bekommen es auch die DS-video-Apps).
func (h *Handler) mainPlaylist(x *req, itemID string) {
	if j, _, ok := h.hlsStart(x, itemID); ok {
		h.S.HLS.ServeFileRanged(x.w, x.r, j, "index.m3u8", "hls/"+j.ID+"/")
	}
}

func (h *Handler) hlsStart(x *req, itemID string) (*stream.Job, string, bool) {
	if h.S.HLS == nil || h.S.HLS.FFmpeg == "" {
		jsonError(x.w, http.StatusServiceUnavailable, stream.ErrNoFFmpeg.Error())
		return nil, "", false
	}
	if h.S.ConnBlocked(x.sess) {
		jsonError(x.w, http.StatusForbidden, "Konto ist auf einem anderen Gerät aktiv")
		return nil, "", false
	}
	// Dieselbe Abfrage noch einmal (Safari/AVPlayer laden die Adresse mehrfach,
	// zuerst nur Teilbereiche): denselben Job weiterverwenden. Ein neuer Job
	// würde den vorigen beenden – der Player hielte dann eine tote Liste.
	psid := x.Q("PlaySessionId")
	skey := psid
	if skey == "" {
		skey = "auto:" + x.auth.DeviceID + ":" + itemID
	}
	sig := itemID + "?" + x.r.URL.RawQuery
	h.mu.Lock()
	ps := h.sessions[skey]
	h.mu.Unlock()
	if ps != nil && ps.Sig == sig && ps.Master != "" {
		if j, ok := h.S.HLS.Get(ps.Job); ok {
			return j, ps.Master, true
		}
	}
	f, it, ok := h.fileFor(x, itemID, x.Q("MediaSourceId"))
	if !ok {
		http.NotFound(x.w, x.r)
		return nil, "", false
	}
	if f.Probe.NeedsProbe(h.S.FFprobe) && h.S.FFprobe != "" {
		f.Probe = library.RunProbe(h.S.FFprobe, f.Path)
		h.S.Lib.SetProbe(f.ID, f.Probe)
	}
	caps := stream.Caps{Name: "android", Containers: map[string]bool{}, Video: map[string]bool{"h264": true}, Audio: map[string]bool{"aac": true}}
	if isApple(x.auth.Client + " " + x.dev.Client + " " + x.dev.DeviceName + " " + x.r.UserAgent()) {
		caps.Name = "apple" // AVPlayer: AC-3/E-AC-3 dürfen unverändert in die HLS-Liste
	}
	for _, v := range x.QList("VideoCodec") {
		caps.Video[strings.ToLower(v)] = true
	}
	for _, a := range x.QList("AudioCodec") {
		caps.Audio[strings.ToLower(a)] = true
	}
	d := stream.Decide(f.Probe, f.Path, caps)
	if d.Mode == stream.ModeDirect {
		d = stream.Decision{Mode: stream.ModeRemux, CopyVideo: true, CopyAudio: f.Probe == nil || stream.HLSAudioCopy(f.Probe.AudioCodec, caps),
			HEVC: f.Probe != nil && f.Probe.VideoCodec == "hevc", Client: caps.Name, Reason: "Jellyfin-App: HLS gewünscht"}
	}
	if v, ok := x.QBool("AllowVideoStreamCopy"); ok && !v && d.Mode != stream.ModeTranscode {
		d = stream.Decision{Mode: stream.ModeTranscode, CopyAudio: d.CopyAudio, Client: caps.Name, Reason: "Jellyfin-App: Umwandlung gewünscht"}
	}
	if mb := x.QInt64("MaxStreamingBitrate", 0); mb > 0 {
		if br := fileBitrate(f); br > mb && d.CopyVideo {
			d = stream.Decision{Mode: stream.ModeTranscode, CopyAudio: d.CopyAudio, Client: caps.Name, Reason: "Bitrate über dem Limit der App"}
		}
		if d.Mode == stream.ModeTranscode {
			switch {
			case mb < 2_500_000:
				d.MaxHeight = 480
			case mb < 6_000_000:
				d.MaxHeight = 720
			}
			// Videobitrate so wählen, dass Bild und Ton durch die Leitung passen
			if mb < 20_000_000 {
				d.MaxRate = max(mb*85/100-200_000, 400_000)
				d.Reason += fmt.Sprintf(", höchstens %.1f Mbit/s", float64(d.MaxRate)/1e6)
			}
		}
	}
	if f.Probe != nil && f.Probe.Duration > 0 {
		d.Duration = f.Probe.Duration
	}
	// Die App wünscht MPEG-TS (z.B. die Mediathek der App „Jellyfin“ auf dem
	// iPhone, wie bei Jellyfin selbst): für H.264; HEVC bleibt fMP4.
	if strings.EqualFold(x.Q("SegmentContainer"), "ts") && !(d.CopyVideo && d.HEVC) {
		d.TS = true
		d.Reason += ", MPEG-TS"
	}
	audio, acodec := 0, ""
	if f.Probe != nil {
		acodec = f.Probe.AudioCodec
	}
	if ai := x.QInt("AudioStreamIndex", -1); ai >= 0 {
		for _, t := range h.tracks(x, it, f, nil, true) {
			if t.isAudio && t.stream["Index"] == ai {
				audio, acodec = t.audio, t.codec
			}
		}
	}
	// Ton nur unverändert übernehmen, wenn HLS es erlaubt und die App das
	// Format nennt (bzw. keine Liste schickt) – und die App es nicht abwählt.
	if acodec != "" {
		listed := len(x.QList("AudioCodec")) == 0 || caps.Audio[strings.ToLower(acodec)]
		d.CopyAudio = stream.HLSAudioCopy(acodec, caps) && listed
	}
	if v, ok := x.QBool("AllowAudioStreamCopy"); ok && !v {
		d.CopyAudio = false
	}
	if !d.CopyAudio && d.Mode == stream.ModeRemux && !strings.Contains(d.Reason, "→ AAC") {
		if strings.Contains(d.Reason, " unverändert") {
			d.Reason = strings.Replace(d.Reason, " unverändert", " → AAC", 1)
		} else {
			d.Reason += ", Ton → AAC"
		}
	}
	start := float64(x.QInt64("StartTimeTicks", 0)) / ticksPerSecond
	if d.Duration == 0 {
		start = 0 // ohne Laufzeit keine durchsuchbare Liste – Positionen wären sonst verschoben
	}
	h.stopSession(skey)
	j, err := h.S.HLS.Start(x.sess.Account, f.Path, d, start, audio)
	if err != nil {
		log.Printf("jellyfin: Umwandlung für %q nicht gestartet: %v", x.sess.Account, err)
		jsonError(x.w, http.StatusServiceUnavailable, err.Error())
		return nil, "", false
	}
	h.S.Conns().Play(x.sess.Key, j.ID, h.title(it))
	log.Printf("jellyfin: %s für %q (%s): %s", d.Mode, x.sess.Account, x.dev.label(), d.Reason)
	master := h.masterPlaylist(j, f, d, acodec)
	h.mu.Lock()
	h.sessions[skey] = &playSession{Job: j.ID, Key: x.sess.Key, Created: time.Now(), Sig: sig, Master: master}
	h.mu.Unlock()
	return j, master, true
}

// masterPlaylist: Übersichtsliste wie bei Jellyfin – mit BANDWIDTH, CODECS,
// RESOLUTION, FRAME-RATE und VIDEO-RANGE. Der Apple-Player auf dem iPhone
// braucht sie: Lädt er die Segmentliste direkt, fragt er fMP4-Segmente endlos
// ab; fehlen bei HEVC/HDR CODECS, Auflösung oder VIDEO-RANGE, spielt er nicht
// (Test mit iOS 18). CODECS stammt aus der tatsächlichen Ausgabe von FFmpeg.
func (h *Handler) masterPlaylist(j *stream.Job, f library.File, d stream.Decision, acodec string) string {
	w, ht, fps := 0, 0, 0.0
	if f.Probe != nil {
		w, ht, fps = f.Probe.Width, f.Probe.Height, f.Probe.FrameRate
	}
	var peak, avg int64
	if d.CopyVideo {
		avg = fileBitrate(f)
		if avg <= 0 {
			avg = 20_000_000
		}
		peak = avg * 3 / 2
	} else {
		rate := d.MaxRate
		if rate <= 0 {
			rate = 8_000_000
		}
		peak, avg = rate+192_000, (rate+192_000)*4/5
		// Höhe nach der Umwandlung (höchstens MaxHeight bzw. 1080), gerade Breite
		maxh := d.MaxHeight
		if maxh <= 0 {
			maxh = 1080
		}
		if ht > maxh && ht > 0 {
			w, ht = (w*maxh/ht+1)/2*2, maxh
		}
	}
	attrs := []string{fmt.Sprintf("BANDWIDTH=%d", peak), fmt.Sprintf("AVERAGE-BANDWIDTH=%d", avg)}
	wait := 12 * time.Second
	if d.CopyVideo {
		wait = 8 * time.Second
	}
	codecs := []string{}
	if v := j.VideoCodecTag(wait); v != "" {
		codecs = append(codecs, v)
		if a := h.S.HLS.AudioCodecTag(acodec, d.CopyAudio, d.Client); a != "" && acodec != "" {
			codecs = append(codecs, a)
		}
	}
	if len(codecs) > 0 {
		attrs = append(attrs, `CODECS="`+strings.Join(codecs, ",")+`"`)
	}
	if w > 0 && ht > 0 {
		attrs = append(attrs, fmt.Sprintf("RESOLUTION=%dx%d", w, ht))
	}
	if fps > 0 {
		attrs = append(attrs, fmt.Sprintf("FRAME-RATE=%.3f", fps))
	}
	attrs = append(attrs, "VIDEO-RANGE="+h.S.HLS.VideoRange(f.Path, d.CopyVideo))
	return "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-STREAM-INF:" + strings.Join(attrs, ",") + "\nhls/" + j.ID + "/index.m3u8\n"
}

// hlsFile: /videos/{id}/hls/{job}/{datei} – die Job-ID ist das Zugriffstoken.
func (h *Handler) hlsFile(x *req) {
	id, name := x.raw[3], x.raw[4]
	if h.S.HLS == nil {
		http.NotFound(x.w, x.r)
		return
	}
	j, ok := h.S.HLS.Get(id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	w := x.w
	if key, blocked := h.S.Conns().Stream(id); blocked {
		http.Error(w, "Konto ist auf einem anderen Gerät aktiv", http.StatusForbidden)
		return
	} else if key != "" {
		gone := h.S.Conns().Begin(key, true)
		defer h.S.Conns().End(key, true)
		w = vs.GuardWriter(w, gone)
	}
	h.S.HLS.ServeFileRanged(w, x.r, j, name, "")
}

func (h *Handler) stopSession(psid string) {
	if psid == "" {
		return
	}
	h.mu.Lock()
	ps, ok := h.sessions[psid]
	delete(h.sessions, psid)
	h.mu.Unlock()
	if ok && h.S.HLS != nil {
		h.S.HLS.Stop(ps.Job)
	}
}

func (h *Handler) stopSessionsOf(key string) {
	h.mu.Lock()
	var jobs []string
	for id, ps := range h.sessions {
		if ps.Key == key {
			jobs = append(jobs, ps.Job)
			delete(h.sessions, id)
		}
	}
	h.mu.Unlock()
	for _, j := range jobs {
		if h.S.HLS != nil {
			h.S.HLS.Stop(j)
		}
	}
}

func (h *Handler) gcSessions() {
	h.tr.gc()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ps := range h.sessions {
		if time.Since(ps.Created) > 12*time.Hour {
			delete(h.sessions, id)
		}
	}
}

// ---------- Untertitel ----------

func (h *Handler) subtitle(x *req, itemID, msid, index, file string) {
	f, it, ok := h.fileFor(x, itemID, msid)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	idx, err := strconv.Atoi(index)
	if err != nil {
		http.NotFound(x.w, x.r)
		return
	}
	var sub *track
	for _, t := range h.tracks(x, it, f, nil, true) {
		if t.isSub && t.stream["Index"] == idx {
			t := t
			sub = &t
		}
	}
	if sub == nil || sub.subID == "" {
		http.NotFound(x.w, x.r)
		return
	}
	data, err := h.S.SubtitleText(f, sub.subID)
	if err != nil {
		http.Error(x.w, "Untertitel nicht lesbar", http.StatusInternalServerError)
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	switch ext {
	case "vtt":
		x.w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "WEBVTT") {
			data = srtToVTT(data)
		}
	default:
		x.w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	}
	_, _ = x.w.Write(data)
}

// srtToVTT: SRT → WebVTT (Kopfzeile, Komma → Punkt in Zeitangaben).
func srtToVTT(b []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var sb strings.Builder
	sb.WriteString("WEBVTT\n\n")
	for _, l := range lines {
		if strings.Contains(l, "-->") {
			l = strings.ReplaceAll(l, ",", ".")
		}
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// ---------- Fortschritt, gesehen, Favoriten ----------

type progressBody struct {
	ItemId        string
	MediaSourceId string
	PositionTicks *int64
	PlaySessionId string
	IsPaused      bool
}

func (h *Handler) readProgress(x *req, itemID string) progressBody {
	var b progressBody
	readBody(x.r, &b)
	if itemID != "" {
		b.ItemId = itemID
	}
	if v := x.Q("MediaSourceId"); v != "" {
		b.MediaSourceId = v
	}
	if v := x.QInt64("PositionTicks", -1); v >= 0 {
		b.PositionTicks = &v
	}
	if v := x.Q("PlaySessionId"); v != "" {
		b.PlaySessionId = v
	}
	return b
}

// report: event start | progress | stopped.
func (h *Handler) report(x *req, itemID, event string) {
	b := h.readProgress(x, itemID)
	f, it, ok := h.fileFor(x, b.ItemId, b.MediaSourceId)
	if ok && !strings.HasPrefix(b.PlaySessionId, dlSessionPrefix) {
		if event == "start" {
			h.S.Conns().Play(x.sess.Key, "jfplay:"+b.PlaySessionId, h.title(it))
		}
		if b.PositionTicks != nil && (*b.PositionTicks > 0 || event != "start") {
			pos := int(*b.PositionTicks / ticksPerSecond)
			if err := h.S.Lib.SetWatch(x.sess.Account, f.ID, pos); err == nil && event == "stopped" {
				log.Printf("jellyfin: Position %ds für Datei %d gespeichert (Konto %q, %s)", pos, f.ID, x.sess.Account, x.dev.label())
			}
		}
	}
	if event == "stopped" {
		h.stopSession(b.PlaySessionId)
	}
	noContent(x.w)
}

// setPlayed markiert ein Objekt (bei Serien/Staffeln alle Folgen).
func (h *Handler) setPlayed(x *req, id string, played bool) {
	e, ok := h.resolve(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	var items []library.Item
	switch e.kind {
	case kItem:
		items = []library.Item{e.it}
	case kShow, kSeason:
		for _, ep := range h.episodes(x)[e.sh.ID] {
			if e.kind == kShow || ep.Season == e.season {
				items = append(items, ep)
			}
		}
	}
	for _, it := range items {
		files := h.S.Lib.Files(it)
		if !played {
			for _, f := range files {
				_ = h.S.Lib.ClearWatch(x.sess.Account, f.ID)
			}
			continue
		}
		if h.state(x, it).Played || len(files) == 0 {
			continue
		}
		f := files[0]
		if st := h.state(x, it); st.FileID != 0 {
			for _, g := range files {
				if g.ID == st.FileID {
					f = g
				}
			}
		}
		if dur := fileDuration(f, it); dur > 0 {
			_ = h.S.Lib.SetWatch(x.sess.Account, f.ID, int(math.Ceil(dur)))
		}
	}
	x.states = nil
	writeJSON(x.w, h.userData(x, e))
}

func (h *Handler) setFavorite(x *req, id string, on bool) {
	e, ok := h.resolve(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	h.store.setFavorite(x.sess.Account, e.favKey(), on)
	writeJSON(x.w, h.userData(x, e))
}

func (h *Handler) itemUserData(x *req, id string) {
	e, ok := h.resolve(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	if x.r.Method == http.MethodPost {
		var b struct {
			Played                *bool
			IsFavorite            *bool
			PlaybackPositionTicks *int64
		}
		readBody(x.r, &b)
		if b.IsFavorite != nil {
			h.store.setFavorite(x.sess.Account, e.favKey(), *b.IsFavorite)
		}
		if b.PlaybackPositionTicks != nil && e.kind == kItem {
			if f, _, ok := h.fileFor(x, id, ""); ok {
				_ = h.S.Lib.SetWatch(x.sess.Account, f.ID, int(*b.PlaybackPositionTicks/ticksPerSecond))
			}
		}
		if b.Played != nil {
			x.states = nil
			h.setPlayed(x, id, *b.Played)
			return
		}
		x.states = nil
	}
	writeJSON(x.w, h.userData(x, e))
}

// ---------- Anzeigeeinstellungen der Apps ----------

func (h *Handler) displayPrefs(x *req, id string) {
	key := strings.ToLower(id + "|" + x.Q("client"))
	if x.r.Method == http.MethodPost {
		var raw json.RawMessage
		readBody(x.r, &raw)
		if len(raw) > 0 && len(raw) < 64<<10 && json.Valid(raw) {
			h.store.setPrefs(x.sess.Account, key, raw)
		}
		noContent(x.w)
		return
	}
	if raw, ok := h.store.prefs(x.sess.Account, key); ok {
		x.w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = x.w.Write(raw)
		return
	}
	writeJSON(x.w, map[string]any{"Id": id, "SortBy": "SortName", "RememberIndexing": false, "PrimaryImageHeight": 250,
		"PrimaryImageWidth": 250, "CustomPrefs": map[string]string{}, "ScrollDirection": "Horizontal", "ShowBackdrop": true,
		"RememberSorting": false, "SortOrder": "Ascending", "ShowSidebar": false, "Client": x.Q("client")})
}

var reApple = regexp.MustCompile(`(?i)iphone|ipad|ipod|macintosh|mac os x|tvos|apple ?tv|swiftfin|infuse`)

// isApple: App bzw. Browser auf einem Apple-Gerät (nicht „AppleWebKit“ unter Android).
func isApple(s string) bool {
	return reApple.MatchString(s) && !strings.Contains(strings.ToLower(s), "android")
}
