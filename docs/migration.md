# Migration aus Video Station

**Status: noch nicht implementiert.**

Vorgehen (geplant, Phase 4):

1. Nur lesend auf eine vorhandene Video-Station-Datenbank zugreifen; Struktur
   ausschließlich aus der eigenen, rechtmäßig vorhandenen Installation ermitteln
   (Speicherort und Schema sind hier **unbekannt** und werden zuerst erhoben).
2. Vor jedem Import: Sicherung von `var/library.json`.
3. Übernahme: Wiedergabepositionen, gesehen-Status, Sammlungen/Favoriten,
   manuelle Metadaten/Poster. Zuordnung über den Dateipfad.
4. `.vsmeta`-Dateien neben den Videos (von Video Station geschriebene Metadaten)
   als zusätzliche Quelle prüfen – Format unbekannt, öffentliche Analysen
   existieren (z.B. `vsmetaEncoder`), vor Nutzung verifizieren.
